package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// detectorWith is driftHarness.detector with a caller-supplied notifier
// (possibly nil), so tests can see what was notified.
func (h *driftHarness) detectorWith(cfg DriftConfig, n DriftNotifier) *DriftDetector {
	h.t.Helper()
	d, err := h.ts.NewDriftDetector(cfg, n)
	if err != nil {
		h.t.Fatalf("creating detector: %v", err)
	}
	d.now = func() time.Time { return h.clock }
	if err := d.seed(context.Background()); err != nil {
		h.t.Fatalf("seeding detector: %v", err)
	}
	return d
}

// feed returns every persisted drift event, newest first.
func (h *driftHarness) feed() []storage.DriftEvent {
	h.t.Helper()
	events, err := h.store.Drift().ListEvents(context.Background(), storage.DriftEventFilter{})
	if err != nil {
		h.t.Fatalf("listing drift events: %v", err)
	}
	return events
}

func (n *recordingNotifier) all() (appeared, disappeared []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, r := range n.reports {
		appeared = append(appeared, edgeIDs(r.Appeared)...)
		disappeared = append(disappeared, edgeIDs(r.Disappeared)...)
	}
	return appeared, disappeared
}

func feedIDs(events []storage.DriftEvent) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, string(ev.Kind)+":"+ev.ConnectionID)
	}
	return out
}

func assertFeed(t *testing.T, got []storage.DriftEvent, want ...string) {
	t.Helper()
	ids := feedIDs(got)
	if len(ids) != len(want) {
		t.Fatalf("feed: got %v, want %v", ids, want)
	}
	for i := range ids {
		if ids[i] != want[i] {
			t.Fatalf("feed: got %v, want %v", ids, want)
		}
	}
}

// TestDriftPersistsPublishesAndNotifies: one pass records the change in the
// feed with display names, pushes it to live subscribers, and notifies.
func TestDriftPersistsPublishesAndNotifies(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	ctx := context.Background()
	h.see("api", "db", 5432)
	h.advance(2 * time.Hour)

	// The source is in current state; the target was pruned from it and is
	// only known by name from history.
	if err := h.store.Services().Upsert(ctx, &storage.Service{ID: "api", Name: "api-gateway", Healthy: true}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.History().RecordService(ctx, &storage.Service{ID: "93.184.216.34:443", Name: "example.com"}, h.clock, 5*time.Minute); err != nil {
		t.Fatal(err)
	}

	sub := h.ts.eventBus.Subscribe("test")
	n := &recordingNotifier{}
	d := h.detectorWith(DriftConfig{Warmup: time.Hour}, n)

	h.advance(time.Minute)
	h.see("api", "93.184.216.34:443", 443)
	h.advance(4 * time.Minute)
	d.step(ctx)

	feed := h.feed()
	assertFeed(t, feed, "appeared:api->93.184.216.34:443")
	ev := feed[0]
	if ev.SourceName != "api-gateway" || ev.TargetName != "example.com" {
		t.Errorf("names = %q -> %q, want api-gateway -> example.com", ev.SourceName, ev.TargetName)
	}
	if !ev.External || ev.Port != 443 || ev.Protocol != "tcp" || !ev.DetectedAt.Equal(h.clock) {
		t.Errorf("unexpected event: %+v", ev)
	}

	select {
	case got := <-sub.Events:
		events, ok := got.Data.([]storage.DriftEvent)
		if got.Type != EventDriftDetected || !ok || len(events) != 1 || events[0].ID != ev.ID {
			t.Errorf("unexpected bus event: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no drift event published")
	}

	if appeared, _ := n.all(); len(appeared) != 1 {
		t.Errorf("notifier got %v, want the one new edge", appeared)
	}
	if st := h.ts.GetDriftStatus(); !st.Running || !st.LastCheck.Equal(h.clock) {
		t.Errorf("status not published: %+v", st)
	}
}

// TestDriftWithoutNotifier: detection no longer needs a webhook.
func TestDriftWithoutNotifier(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	h.see("api", "db", 5432)
	h.advance(2 * time.Hour)
	d := h.detectorWith(DriftConfig{Warmup: time.Hour}, nil)

	h.see("api", "cache", 6379)
	h.advance(5 * time.Minute)
	d.step(context.Background())
	assertFeed(t, h.feed(), "appeared:api->cache")
}

// TestDriftRestartResumesFromCheckpoint: an edge that appears after the last
// pass but before a restart used to be absorbed into the new process's
// baseline and never reported. Resuming from the checkpoint reports it.
func TestDriftRestartResumesFromCheckpoint(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	ctx := context.Background()
	h.see("api", "db", 5432)
	h.advance(2 * time.Hour)

	before := h.detectorWith(DriftConfig{Warmup: time.Hour}, &recordingNotifier{})
	h.advance(5 * time.Minute)
	h.see("api", "db", 5432)
	before.step(ctx) // nothing new; checkpoint written

	h.advance(time.Minute)
	h.see("api", "cache", 6379) // appears, then the process stops before its next pass

	h.advance(20 * time.Minute) // downtime
	n := &recordingNotifier{}
	after := h.detectorWith(DriftConfig{Warmup: time.Hour}, n)
	after.step(ctx)

	assertFeed(t, h.feed(), "appeared:api->cache")
	if appeared, _ := n.all(); len(appeared) != 1 || appeared[0] != "api->cache" {
		t.Errorf("notified %v, want [api->cache]", appeared)
	}

	// And the resumed detector does not then announce it again.
	h.advance(5 * time.Minute)
	h.see("api", "cache", 6379)
	after.step(ctx)
	if got := len(h.feed()); got != 1 {
		t.Errorf("feed grew to %d events", got)
	}
}

// TestDriftRestartDoesNotDuplicate: resuming re-evaluates the edges near the
// checkpoint, some of which were already reported. They must not be stored or
// notified twice.
func TestDriftRestartDoesNotDuplicate(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	ctx := context.Background()
	h.see("api", "db", 5432)
	h.advance(2 * time.Hour)

	first := h.detectorWith(DriftConfig{Warmup: time.Hour}, &recordingNotifier{})
	h.see("api", "cache", 6379)
	h.advance(2 * time.Minute)
	first.step(ctx) // reports api->cache, checkpoint 2 minutes after it was first seen
	assertFeed(t, h.feed(), "appeared:api->cache")

	h.advance(time.Minute)
	n := &recordingNotifier{}
	second := h.detectorWith(DriftConfig{Warmup: time.Hour}, n)
	h.see("api", "cache", 6379)
	h.advance(time.Minute)
	second.step(ctx)

	assertFeed(t, h.feed(), "appeared:api->cache")
	if appeared, disappeared := n.all(); len(appeared)+len(disappeared) != 0 {
		t.Errorf("restart re-notified: appeared=%v disappeared=%v", appeared, disappeared)
	}
}

// TestDriftRestartReportsDisappearanceOnce: an edge that crossed GoneAfter
// while the backend was restarting is reported by the resumed detector, and
// an edge already reported gone before the restart is not reported again.
func TestDriftRestartReportsDisappearanceOnce(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	ctx := context.Background()
	cfg := DriftConfig{GoneAfter: 2 * time.Hour, Warmup: 0}
	h.see("api", "db", 5432)
	h.see("api", "legacy", 8080)
	h.see("api", "old", 9000)

	d := h.detectorWith(cfg, &recordingNotifier{})
	// api->old goes quiet first and is reported; api->legacy a little later.
	for i := 0; i < 2; i++ {
		h.advance(30 * time.Minute)
		h.see("api", "db", 5432)
		h.see("api", "legacy", 8080)
	}
	for i := 0; i < 4; i++ {
		h.advance(30 * time.Minute)
		h.see("api", "db", 5432)
	}
	d.step(ctx)
	assertFeed(t, h.feed(), "disappeared:api->old")

	// api->legacy crosses the threshold during a restart.
	for i := 0; i < 2; i++ {
		h.advance(30 * time.Minute)
		h.see("api", "db", 5432)
	}
	n := &recordingNotifier{}
	d = h.detectorWith(cfg, n)
	d.step(ctx)

	assertFeed(t, h.feed(), "disappeared:api->legacy", "disappeared:api->old")
	if _, disappeared := n.all(); len(disappeared) != 1 || disappeared[0] != "api->legacy" {
		t.Errorf("notified %v, want only [api->legacy]", disappeared)
	}

	// Another restart: both are old news.
	h.advance(30 * time.Minute)
	h.see("api", "db", 5432)
	n = &recordingNotifier{}
	d = h.detectorWith(cfg, n)
	d.step(ctx)
	if appeared, disappeared := n.all(); len(appeared)+len(disappeared) != 0 {
		t.Errorf("second restart re-announced: appeared=%v disappeared=%v", appeared, disappeared)
	}
	if got := len(h.feed()); got != 2 {
		t.Errorf("feed has %d events, want 2", got)
	}
}

// TestDriftStaleCheckpointIsIgnored: a checkpoint older than retention would
// make everything first seen in the window look new.
func TestDriftStaleCheckpointIsIgnored(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	ctx := context.Background()
	if err := h.store.Drift().SetCheckpoint(ctx, h.clock.Add(-40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	h.see("api", "db", 5432)
	h.advance(2 * time.Hour)
	h.see("api", "cache", 6379)
	h.advance(time.Minute)

	d := h.detectorWith(DriftConfig{Warmup: 0}, nil)
	h.advance(5 * time.Minute)
	d.step(ctx)
	if feed := h.feed(); len(feed) != 0 {
		t.Errorf("stale checkpoint caused a flood: %v", feedIDs(feed))
	}
}

// TestDriftTwoReplicasRecordOnce: two backends over one database both detect
// the same change; it is stored and notified once.
func TestDriftTwoReplicasRecordOnce(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	ctx := context.Background()
	h.see("api", "db", 5432)
	h.advance(2 * time.Hour)

	na, nb := &recordingNotifier{}, &recordingNotifier{}
	a := h.detectorWith(DriftConfig{Warmup: time.Hour}, na)
	b := h.detectorWith(DriftConfig{Warmup: time.Hour}, nb)
	h.see("api", "cache", 6379)
	h.advance(5 * time.Minute)
	a.step(ctx)
	b.step(ctx)

	assertFeed(t, h.feed(), "appeared:api->cache")
	aa, _ := na.all()
	ab, _ := nb.all()
	if len(aa)+len(ab) != 1 {
		t.Errorf("notified %d times, want once (a=%v b=%v)", len(aa)+len(ab), aa, ab)
	}
}

// failingDriftRepo fails every write, recording whether the checkpoint was
// attempted.
type failingDriftRepo struct {
	storage.DriftRepository
	checkpointed bool
}

func (f *failingDriftRepo) InsertEvents(context.Context, []storage.DriftEvent) ([]storage.DriftEvent, error) {
	return nil, errors.New("disk full")
}

func (f *failingDriftRepo) SetCheckpoint(context.Context, time.Time) error {
	f.checkpointed = true
	return nil
}

// TestDriftFeedFailureStillNotifies: a feed that cannot be written must not
// silence the webhook, and must not advance the checkpoint past changes it
// failed to record.
func TestDriftFeedFailureStillNotifies(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	h.see("api", "db", 5432)
	h.advance(2 * time.Hour)
	n := &recordingNotifier{}
	d := h.detectorWith(DriftConfig{Warmup: time.Hour}, n)
	repo := &failingDriftRepo{DriftRepository: h.store.Drift()}
	d.events = repo

	h.see("api", "cache", 6379)
	h.advance(5 * time.Minute)
	d.step(context.Background())

	if appeared, _ := n.all(); len(appeared) != 1 {
		t.Errorf("notifier got %v, want the new edge despite the feed failing", appeared)
	}
	if repo.checkpointed {
		t.Error("checkpoint advanced past changes that were not recorded")
	}
}

func TestListDriftEventsPaginates(t *testing.T) {
	ts, _ := newHistoryTestService(t, true)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)
	var events []storage.DriftEvent
	for i := 0; i < 5; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		events = append(events, storage.DriftEvent{
			DetectedAt: at, Kind: storage.DriftAppeared, ConnectionID: string(rune('a'+i)) + "->x",
			SourceID: string(rune('a' + i)), TargetID: "x", FirstSeen: at, LastSeen: at,
			Fingerprint: string(rune('a' + i)),
		})
	}
	if _, err := ts.ImportDriftEvents(ctx, events); err != nil {
		t.Fatal(err)
	}

	page, err := ts.ListDriftEvents(ctx, storage.DriftEventFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.Events[0].SourceID != "e" || page.Next == nil {
		t.Fatalf("first page: %+v", page)
	}
	page, _ = ts.ListDriftEvents(ctx, storage.DriftEventFilter{Limit: 2, Before: page.Next})
	page, _ = ts.ListDriftEvents(ctx, storage.DriftEventFilter{Limit: 2, Before: page.Next})
	if len(page.Events) != 1 || page.Events[0].SourceID != "a" || page.Next != nil {
		t.Fatalf("last page: %+v", page)
	}

	if _, err := ts.ListDriftEvents(ctx, storage.DriftEventFilter{Kind: "moved"}); !errors.Is(err, storage.ErrInvalidInput) {
		t.Errorf("unknown kind: got %v, want ErrInvalidInput", err)
	}
	disabled, _ := newHistoryTestService(t, false)
	if _, err := disabled.ListDriftEvents(ctx, storage.DriftEventFilter{}); !errors.Is(err, ErrHistoryDisabled) {
		t.Errorf("history disabled: got %v", err)
	}
}
