package llm

import (
	"sync"
	"testing"
	"time"
)

// TestManagerConcurrentUpdateAndRead is a regression test for an unguarded map
// in Manager. POST /api/v1/ai/config calls UpdateConfig while other requests
// call Status/GetProvider, and the resulting concurrent map access is a Go
// runtime fatal error ("concurrent map writes") rather than a panic — the
// recovery middleware cannot contain it, so the whole backend dies.
//
// Run with -race to catch the data race; without -race the runtime's own map
// check is what used to abort the process.
func TestManagerConcurrentUpdateAndRead(t *testing.T) {
	m := NewManager(&Config{})

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					m.UpdateConfig(&Config{OpenAIAPIKey: "test-key"})
				}
			}
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = m.Status()
					_, _ = m.GetProvider(ProviderOpenAI)
					_ = m.GetConfiguredProviders()
					_, _ = m.GetDefaultProvider()
					_ = m.Config()
				}
			}
		}()
	}

	time.Sleep(500 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestUpdateConfigNilIsSafe guards the nil path, since HandleConfig builds the
// config from a request body that may deserialize to nothing useful.
func TestUpdateConfigNilIsSafe(t *testing.T) {
	m := NewManager(nil)
	m.UpdateConfig(nil)

	if got := m.Config(); got == nil {
		t.Fatal("Config() returned nil after UpdateConfig(nil)")
	}
	if got := len(m.Status()); got == 0 {
		t.Fatal("expected providers to be present after UpdateConfig(nil)")
	}
}
