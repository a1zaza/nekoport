package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestEntrypointDefaultKeyAndExplicitArguments(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "deploy", "entrypoint.sh"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "stub")
	script := filepath.Join(dir, "entrypoint.sh")
	capture := filepath.Join(dir, "args")
	if err = os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$NEKOPORT_TEST_ARGS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	replaced := strings.ReplaceAll(string(source), "/usr/local/bin/nekoport", `"$NEKOPORT_TEST_BINARY"`)
	if err = os.WriteFile(script, []byte(replaced), 0700); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, listen string
		input, want  []string
	}{
		{name: "single directory defaults", want: []string{"serve", "-data", "/var/lib/nekoport", "-listen", "0.0.0.0:8080", "-master-key-file", "/var/lib/nekoport/master.key"}},
		{name: "listen override", listen: "0.0.0.0:9090", want: []string{"serve", "-data", "/var/lib/nekoport", "-listen", "0.0.0.0:9090", "-master-key-file", "/var/lib/nekoport/master.key"}},
		{name: "explicit arguments", input: []string{"serve", "-data", "/data with spaces", "-listen", "127.0.0.1:9090"}, want: []string{"serve", "-data", "/data with spaces", "-listen", "127.0.0.1:9090"}},
		{name: "version", input: []string{"version"}, want: []string{"version"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("sh", append([]string{script}, test.input...)...)
			command.Env = append(os.Environ(), "NEKOPORT_TEST_BINARY="+binary, "NEKOPORT_TEST_ARGS="+capture, "NEKOPORT_LISTEN_ADDRESS="+test.listen)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("entrypoint failed: %v %s", err, output)
			}
			data, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("arguments: %q, want %q", got, test.want)
			}
		})
	}
}
