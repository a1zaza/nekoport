package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/a1zaza/nekoport/internal/app"
	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/secrets"
	"github.com/a1zaza/nekoport/internal/storage"
	"github.com/a1zaza/nekoport/internal/transport"
	"github.com/a1zaza/nekoport/web"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
)

var version = "0.1.0"

func main() {
	if e := run(os.Args[1:]); e != nil {
		log.Print(e)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("用法：nekoport serve | init | keygen | healthcheck | version")
	}
	if args[0] == "version" {
		fmt.Println("nekoport", version)
		return nil
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	data := f.String("data", ".data", "数据目录")
	key := f.String("master-key-file", "", "只读主密钥文件路径")
	listen := f.String("listen", "127.0.0.1:8080", "HTTP 监听地址")
	username := f.String("username", "", "应用访问用户名")
	stdin := f.Bool("password-stdin", false, "从 stdin 读取密码")
	origin := f.String("origin", "", "额外允许的开发来源，逗号分隔")
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	switch args[0] {
	case "keygen":
		if *key == "" {
			return errors.New("请通过 -master-key-file 指定密钥输出路径")
		}
		return secrets.Generate(*key)
	case "healthcheck":
		c := http.Client{Timeout: 3 * time.Second}
		address := *listen
		if *listen == "127.0.0.1:8080" && os.Getenv("NEKOPORT_LISTEN_ADDRESS") != "" {
			address = os.Getenv("NEKOPORT_LISTEN_ADDRESS")
		}
		if strings.HasPrefix(address, "0.0.0.0:") {
			address = "127.0.0.1:" + strings.TrimPrefix(address, "0.0.0.0:")
		}
		r, e := c.Get("http://" + address + "/health/ready")
		if e != nil {
			return e
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			return errors.New("服务尚未就绪")
		}
		return nil
	case "init", "serve":
	default:
		return errors.New("未知命令")
	}
	if args[0] == "serve" {
		if e := web.Check(); e != nil {
			return e
		}
	}
	s, e := storage.Open(*data)
	if e != nil {
		return e
	}
	defer s.Close()
	if args[0] == "init" {
		if *username == "" || len(*username) > 128 {
			return errors.New("请设置有效的 -username")
		}
		var existing bool
		s.Read(func(st domain.State, _ storage.Index) { existing = st.Settings.Username != "" })
		if existing {
			return errors.New("访问账号已存在，拒绝覆盖")
		}
		var password string
		if *stdin {
			r := bufio.NewReader(io.LimitReader(os.Stdin, 1024))
			password, e = r.ReadString('\n')
			if e != nil && e != io.EOF {
				return e
			}
			password = strings.TrimRight(password, "\r\n")
		} else {
			fmt.Fprint(os.Stderr, "设置应用密码（至少 8 位）：")
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return err
			}
			password = string(b)
			fmt.Fprint(os.Stderr, "再次输入：")
			b, err = term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return err
			}
			if password != string(b) {
				return errors.New("两次密码不一致")
			}
		}
		if len(password) < 8 || len(password) > 72 {
			return errors.New("密码需为 8–72 字节")
		}
		hash, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if e != nil {
			return e
		}
		e = s.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
			v := st.Settings
			v.Username = *username
			v.PasswordHash = string(hash)
			return []storage.Op{storage.Put("settings", "", v)}, nil
		})
		if e == nil {
			fmt.Fprintln(os.Stderr, "应用访问账号初始化成功")
		}
		return e
	}
	var initialized bool
	s.Read(func(st domain.State, _ storage.Index) { initialized = st.Settings.Username != "" })
	if !initialized {
		return errors.New("尚未初始化，请先运行 init -data <目录> -username <用户名>")
	}
	if *key == "" {
		return errors.New("必须提供 -master-key-file")
	}
	v, e := secrets.Open(filepath.Join(s.Root(), "secrets.json"), *key)
	if e != nil {
		return e
	}
	a := app.New(s, v)
	a.Start()
	defer a.Close()
	origins := []string{}
	if *origin != "" {
		origins = strings.Split(*origin, ",")
	}
	handler := transport.New(a, web.Handler(), origins).Handler()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan error, 1)
	go func() { log.Printf("nekoport 监听 %s", *listen); done <- server.ListenAndServe() }()
	select {
	case e := <-done:
		if e != http.ErrServerClosed {
			return e
		}
	case <-ctx.Done():
		stop, c := context.WithTimeout(context.Background(), 25*time.Second)
		defer c()
		if e = server.Shutdown(stop); e != nil {
			server.Close()
		}
	}
	return nil
}
