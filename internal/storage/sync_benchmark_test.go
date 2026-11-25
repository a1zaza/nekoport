package storage

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/a1zaza/nekoport/internal/domain"
)

func BenchmarkHistoricalIndex(b *testing.B) {
	for _, count := range []int{10000, 100000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			runtime.GC()
			var start runtime.MemStats
			runtime.ReadMemStats(&start)
			store, err := Open(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			defer store.Close()
			started := time.Now()
			for offset := 0; offset < count; offset += 100 {
				err = store.Update(func(s domain.State, _ Index) ([]Op, error) {
					ops := make([]Op, 0, 100)
					for n := offset; n < offset+100; n++ {
						id := fmt.Sprint(n)
						ops = append(ops, Put("messages", id, domain.Message{ID: id, AccountID: "a", MailboxID: "inbox", UID: uint32(n + 1), UIDValidity: 1, Subject: "历史邮件容量验证", Date: time.Unix(int64(n), 0)}))
					}
					return ops, nil
				})
				if err != nil {
					b.Fatal(err)
				}
			}
			importTime := time.Since(started)
			runtime.GC()
			var used runtime.MemStats
			runtime.ReadMemStats(&used)
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				err = store.Update(func(s domain.State, _ Index) ([]Op, error) {
					ops := make([]Op, 0, 100)
					for i := 0; i < 100; i++ {
						id := fmt.Sprint(i)
						v := s.Messages[id]
						v.Seen = !v.Seen
						v.LastSeenGeneration = "benchmark-generation"
						ops = append(ops, Put("messages", id, v))
					}
					return ops, nil
				})
				if err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(importTime.Milliseconds()), "import-ms")
			if used.HeapAlloc > start.HeapAlloc {
				b.ReportMetric(float64(used.HeapAlloc-start.HeapAlloc)/(1<<20), "live-MiB")
			}
		})
	}
}
