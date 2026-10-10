package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/evdanil/gotest/internal/monitor"
)

func readTestCopySource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "python-source-v1", "cn-tool-schema1.db"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "history.db")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readTestOpenSource(t *testing.T) (*Store, string) {
	t.Helper()
	path := readTestCopySource(t)
	store, err := OpenReader(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		t.Fatal("OpenReader returned nil for the source fixture")
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close reader: %v", err)
		}
	})
	return store, path
}

func readTestOpenPair(t *testing.T) (*Store, *Store, string) {
	t.Helper()
	path := readTestCopySource(t)
	writer, err := OpenWriter(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := writer.Close(); err != nil {
			t.Errorf("Close writer: %v", err)
		}
	})
	reader, err := OpenReader(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reader.Close(); err != nil {
			t.Errorf("Close reader: %v", err)
		}
	})
	return writer, reader, path
}

func readTestExec(t *testing.T, store *Store, query string, args ...any) {
	t.Helper()
	err := store.withTx(context.Background(), true, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(context.Background(), query, args...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func readTestTime(t *testing.T, seconds float64) time.Time {
	t.Helper()
	value, err := timeFromEpoch(seconds)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestReadViewReturnsSourceFixtureAndExpiresSnapshot(t *testing.T) {
	store, _ := readTestOpenSource(t)
	var escaped *Snapshot
	key := "24156eb156b5bb9d83f75c5e4391db9736dee2ae690bd2c99e8154414d124fff"
	err := store.View(context.Background(), func(snapshot *Snapshot) error {
		escaped = snapshot
		if !snapshot.active || snapshot.through != 3 {
			t.Fatalf("snapshot active=%v through=%d, want active through 3", snapshot.active, snapshot.through)
		}
		request, err := snapshot.Request(key)
		if err != nil {
			return err
		}
		if request == nil {
			t.Fatal("fixture request was not found")
		}
		if request.ID != 1 || request.Runs != 4 || request.Rounds != 3 || request.NewestRun == nil || request.NewestRun.ID != "fixture-running-zero-round" {
			t.Fatalf("request summary = %+v", request.RequestSummary)
		}
		if !reflect.DeepEqual(request.Targets, []string{"api.example.com", "db.example.com"}) || len(request.TCPPorts) != 0 || len(request.Hosts) != 7 {
			t.Fatalf("request positional fields = %+v hosts=%v", request.RequestMeta, request.Hosts)
		}
		if request.FirstRound == nil || request.LastRound == nil || !request.FirstRound.Equal(readTestTime(t, 1700000000.375)) || !request.LastRound.Equal(readTestTime(t, 1700000110.5)) {
			t.Fatalf("request round endpoints = %v, %v", request.FirstRound, request.LastRound)
		}
		matches, err := snapshot.Find(key[:4])
		if err != nil {
			return err
		}
		if len(matches) != 1 || matches[0] != (RequestMatch{ID: 1, Key: key}) {
			t.Fatalf("prefix matches = %+v", matches)
		}
		page, err := snapshot.Rounds(1, RoundFilter{Limit: 1})
		if err != nil {
			return err
		}
		if len(page.Items) != 1 || page.Items[0].ID != 1 || page.Next == nil {
			t.Fatalf("first round page = %+v", page)
		}
		first := page.Items[0]
		if first.Observations != "!RTU.E?" || first.Answered != 3 || len(first.Rows) != 3 {
			t.Fatalf("first stored round = %+v", first)
		}
		if first.Rows[0].Host != "api.example.com" || first.Rows[2].Host != "legacy.example.com" || first.Rows[2].TCP == nil || len(first.Rows[2].TCP) != 0 {
			t.Fatalf("first round detail ordering/TCP = %+v", first.Rows)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if escaped == nil || escaped.active {
		t.Fatal("snapshot remained active after View returned")
	}
	if _, err := escaped.Find(key[:4]); err == nil {
		t.Fatal("escaped snapshot remained usable after View")
	}
}

func TestReadViewCapturesGlobalHighwaterAcrossConcurrentCommit(t *testing.T) {
	writer, reader, _ := readTestOpenPair(t)
	err := reader.View(context.Background(), func(snapshot *Snapshot) error {
		before, err := snapshot.Window(1, WindowFilter{})
		if err != nil {
			return err
		}
		if before.Through != 3 || before.Count != 3 {
			t.Fatalf("initial window = %+v, want through=3 count=3", before)
		}
		readTestExec(t, writer, `INSERT INTO round(run_id,started,observations,answered) VALUES(?,?,?,?)`, "fixture-running-zero-round", 1700000400.0, "???????", 0)
		during, err := snapshot.Window(1, WindowFilter{})
		if err != nil {
			return err
		}
		if during.Through != 3 || during.Count != 3 {
			t.Fatalf("same-view window changed after concurrent commit: %+v", during)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if err := reader.View(context.Background(), func(snapshot *Snapshot) error {
		after, err := snapshot.Window(1, WindowFilter{})
		if err != nil {
			return err
		}
		if after.Through != 4 || after.Count != 4 {
			t.Fatalf("next-view window = %+v, want through=4 count=4", after)
		}
		return nil
	}); err != nil {
		t.Fatalf("next View: %v", err)
	}
}

func TestReadSnapshotHonorsContextCancellation(t *testing.T) {
	reader, _ := readTestOpenSource(t)
	ctx, cancel := context.WithCancel(context.Background())
	err := reader.View(ctx, func(snapshot *Snapshot) error {
		cancel()
		_, err := snapshot.Requests(nil, 1)
		if !errors.Is(err, context.Canceled) {
			return fmt.Errorf("snapshot request after cancel = %v, want context.Canceled", err)
		}
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("View after snapshot cancellation = %v, want context.Canceled", err)
	}
}

func TestReadRequestAndRunKeysetPagesKeepActivityAndRowIDOrder(t *testing.T) {
	writer, reader, _ := readTestOpenPair(t)
	targets, ports, hosts := `["x"]`, `[]`, `["x"]`
	keyA, keyB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	readTestExec(t, writer, `INSERT INTO request(key,targets,tcp_ports,hosts) VALUES(?,?,?,?)`, keyA, targets, ports, hosts)
	readTestExec(t, writer, `INSERT INTO request(key,targets,tcp_ports,hosts) VALUES(?,?,?,?)`, keyB, targets, ports, hosts)
	readTestExec(t, writer, `INSERT INTO run(id,request_id,started,host,pid,heartbeat,status) VALUES(?,?,?,?,?,?,?)`, "reader-run-a", 2, 1700000300.125, "fixture", 123, 1700000300.125, "running")
	readTestExec(t, writer, `INSERT INTO run(id,request_id,started,host,pid,heartbeat,status) VALUES(?,?,?,?,?,?,?)`, "reader-run-b", 2, 1700000300.125, "fixture", 124, 1700000300.125, "running")
	if err := reader.View(context.Background(), func(snapshot *Snapshot) error {
		first, err := snapshot.Requests(nil, 2)
		if err != nil {
			return err
		}
		if len(first.Items) != 2 || first.Items[0].ID != 2 || first.Items[1].ID != 1 || first.Next == nil {
			t.Fatalf("request first page = %+v", first)
		}
		second, err := snapshot.Requests(first.Next, 2)
		if err != nil {
			return err
		}
		if len(second.Items) != 1 || second.Items[0].ID != 3 || second.Next != nil {
			t.Fatalf("request second page = %+v", second)
		}
		matches, err := snapshot.Find("aaaa")
		if err != nil {
			return err
		}
		if len(matches) != 1 || matches[0].ID != 2 || matches[0].Key != keyA {
			t.Fatalf("new request prefix matches = %+v", matches)
		}
		runs, err := snapshot.Runs(2, false, nil, 1)
		if err != nil {
			return err
		}
		if len(runs.Items) != 1 || runs.Items[0].ID != "reader-run-b" || runs.Next == nil {
			t.Fatalf("run first page = %+v", runs)
		}
		next, err := snapshot.Runs(2, false, runs.Next, 1)
		if err != nil {
			return err
		}
		if len(next.Items) != 1 || next.Items[0].ID != "reader-run-a" || next.Next != nil {
			t.Fatalf("run second page = %+v", next)
		}
		return nil
	}); err != nil {
		t.Fatalf("View: %v", err)
	}
}

func TestReadFindReturnsMissingUniqueAmbiguousAndCaseInsensitivePrefixes(t *testing.T) {
	writer, reader, _ := readTestOpenPair(t)
	keyA := "abcd" + strings.Repeat("1", 60)
	keyB := "ABCD" + strings.Repeat("2", 60)
	keyC := "AbCd" + strings.Repeat("3", 60)
	for _, key := range []string{keyA, keyB, keyC} {
		readTestExec(t, writer, `INSERT INTO request(key,targets,tcp_ports,hosts) VALUES(?,?,?,?)`, key, `["x"]`, `[]`, `["x"]`)
	}
	err := reader.View(context.Background(), func(snapshot *Snapshot) error {
		missing, err := snapshot.Find("ffff")
		if err != nil {
			return err
		}
		if len(missing) != 0 {
			t.Fatalf("missing prefix matches = %+v", missing)
		}
		unique, err := snapshot.Find("2415")
		if err != nil {
			return err
		}
		if len(unique) != 1 || unique[0].Key != "24156eb156b5bb9d83f75c5e4391db9736dee2ae690bd2c99e8154414d124fff" {
			t.Fatalf("unique prefix matches = %+v", unique)
		}
		full, err := snapshot.Find(strings.ToUpper(unique[0].Key))
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(full, unique) {
			t.Fatalf("full case-insensitive prefix matches = %+v, want %+v", full, unique)
		}
		ambiguous, err := snapshot.Find("AbCd")
		if err != nil {
			return err
		}
		if len(ambiguous) != 3 || ambiguous[0].Key != keyC || ambiguous[1].Key != keyB || ambiguous[2].Key != keyA {
			t.Fatalf("ambiguous case-insensitive matches = %+v", ambiguous)
		}
		all, err := snapshot.Find("abcd")
		if err != nil {
			return err
		}
		if len(all) != len(ambiguous) {
			t.Fatalf("ambiguous candidates were capped: got %d want %d", len(all), len(ambiguous))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("View: %v", err)
	}
}

func TestRoundPagesOrderStartedThenIDNotCommitOrder(t *testing.T) {
	writer, reader, _ := readTestOpenPair(t)
	for _, started := range []float64{200, 199, 199} {
		readTestExec(t, writer, `INSERT INTO round(run_id,started,observations,answered) VALUES(?,?,?,?)`, "fixture-finished", started, "???????", 0)
	}
	since, before := readTestTime(t, 199), readTestTime(t, 201)
	err := reader.View(context.Background(), func(snapshot *Snapshot) error {
		filter := RoundFilter{WindowFilter: WindowFilter{Since: &since, Before: &before}, Limit: 1}
		first, err := snapshot.Rounds(1, filter)
		if err != nil {
			return err
		}
		if len(first.Items) != 1 || first.Items[0].ID != 5 || first.Next == nil {
			t.Fatalf("first start-ordered page = %+v", first)
		}
		filter.After = first.Next
		second, err := snapshot.Rounds(1, filter)
		if err != nil {
			return err
		}
		if len(second.Items) != 1 || second.Items[0].ID != 6 || second.Items[0].Started != first.Items[0].Started || second.Next == nil {
			t.Fatalf("tied-start page = %+v", second)
		}
		filter.After = second.Next
		third, err := snapshot.Rounds(1, filter)
		if err != nil {
			return err
		}
		if len(third.Items) != 1 || third.Items[0].ID != 4 || third.Next != nil || !third.Items[0].Started.After(second.Items[0].Started) {
			t.Fatalf("later-start page = %+v", third)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("View: %v", err)
	}
}

func TestReadRejectsUnknownAndDerivedRunStatusesWithoutMutation(t *testing.T) {
	const key = "24156eb156b5bb9d83f75c5e4391db9736dee2ae690bd2c99e8154414d124fff"
	for _, status := range []string{"unexpected", "", "lost"} {
		t.Run(fmt.Sprintf("status-%q", status), func(t *testing.T) {
			writer, reader, _ := readTestOpenPair(t)
			readTestExec(t, writer, `UPDATE run SET status=? WHERE id=?`, status, "fixture-running-zero-round")
			checks := []struct {
				name string
				read func(*Snapshot) error
			}{
				{name: "runs", read: func(snapshot *Snapshot) error { _, err := snapshot.Runs(1, false, nil, 500); return err }},
				{name: "request", read: func(snapshot *Snapshot) error { _, err := snapshot.Request(key); return err }},
				{name: "list-summary", read: func(snapshot *Snapshot) error { _, err := snapshot.Requests(nil, 500); return err }},
			}
			for _, check := range checks {
				t.Run(check.name, func(t *testing.T) {
					err := reader.View(context.Background(), check.read)
					if err == nil || !strings.Contains(err.Error(), "unknown stored run status") {
						t.Fatalf("reader accepted status %q: %v", status, err)
					}
				})
			}
			if got := readTestRunStatus(t, writer, "fixture-running-zero-round"); got != status {
				t.Fatalf("readers mutated stored status to %q, want %q", got, status)
			}
		})
	}
}

func TestReadAcceptsAllPersistedRunStatusesInNewestSummaries(t *testing.T) {
	const key = "24156eb156b5bb9d83f75c5e4391db9736dee2ae690bd2c99e8154414d124fff"
	writer, reader, _ := readTestOpenPair(t)
	for _, status := range []string{"running", "finished", "stopped", "failed"} {
		readTestExec(t, writer, `UPDATE run SET status=? WHERE id=?`, status, "fixture-running-zero-round")
		err := reader.View(context.Background(), func(snapshot *Snapshot) error {
			request, err := snapshot.Request(key)
			if err != nil {
				return err
			}
			if request == nil || request.NewestRun == nil || request.NewestRun.Status != status {
				t.Fatalf("Request newest status = %+v, want %q", request, status)
			}
			page, err := snapshot.Requests(nil, 500)
			if err != nil {
				return err
			}
			found := false
			for _, item := range page.Items {
				if item.ID == request.ID {
					found = item.NewestRun != nil && item.NewestRun.Status == status
				}
			}
			if !found {
				t.Fatalf("list summary did not preserve status %q", status)
			}
			runs, err := snapshot.Runs(request.ID, false, nil, 500)
			if err != nil {
				return err
			}
			found = false
			for _, run := range runs.Items {
				if run.ID == "fixture-running-zero-round" {
					found = run.Status == status
				}
			}
			if !found {
				t.Fatalf("run page did not preserve status %q", status)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("View status %q: %v", status, err)
		}
		if got := readTestRunStatus(t, writer, "fixture-running-zero-round"); got != status {
			t.Fatalf("reader changed status %q to %q", status, got)
		}
	}
}

func readTestRunStatus(t *testing.T, store *Store, id string) string {
	t.Helper()
	var status string
	err := store.withTx(context.Background(), false, func(conn *sql.Conn) error {
		return conn.QueryRowContext(context.Background(), `SELECT status FROM run WHERE id=?`, id).Scan(&status)
	})
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func TestReadRoundWindowCursorSpacingAndUnknownRequest(t *testing.T) {
	reader, _ := readTestOpenSource(t)
	if err := reader.View(context.Background(), func(snapshot *Snapshot) error {
		first, err := snapshot.Rounds(1, RoundFilter{Limit: 1})
		if err != nil {
			return err
		}
		if len(first.Items) != 1 || first.Items[0].ID != 1 || first.Next == nil {
			t.Fatalf("first round page = %+v", first)
		}
		second, err := snapshot.Rounds(1, RoundFilter{After: first.Next, Limit: 1})
		if err != nil {
			return err
		}
		if len(second.Items) != 1 || second.Items[0].ID != 2 || second.Next == nil {
			t.Fatalf("second round page = %+v", second)
		}
		window, err := snapshot.Window(1, WindowFilter{})
		if err != nil {
			return err
		}
		if window.Count != 3 || window.Through != 3 || window.First == nil || window.Last == nil || window.First.id != 1 || window.Last.id != 3 {
			t.Fatalf("round window = %+v", window)
		}
		since := readTestTime(t, 1700000001.625)
		before := readTestTime(t, 1700000110.5)
		bounded, err := snapshot.Rounds(1, RoundFilter{WindowFilter: WindowFilter{Since: &since, Before: &before}, Limit: 10})
		if err != nil {
			return err
		}
		if len(bounded.Items) != 1 || bounded.Items[0].ID != 2 {
			t.Fatalf("bounded rounds = %+v", bounded.Items)
		}
		spacing, err := snapshot.Spacing(1, WindowFilter{}, nil)
		if err != nil {
			return err
		}
		if spacing != 0 {
			t.Fatalf("two one-round samples spacing = %s, want 0", spacing)
		}
		extra := readTestTime(t, 1700000003.625)
		spacing, err = snapshot.Spacing(1, WindowFilter{}, &extra)
		if err != nil {
			return err
		}
		if spacing != 1250*time.Millisecond {
			t.Fatalf("lower median spacing = %s, want 1.25s", spacing)
		}
		unknown, err := snapshot.Window(999, WindowFilter{})
		if err != nil {
			return err
		}
		if unknown.Count != 0 || unknown.First != nil || unknown.Last != nil || unknown.Through != 3 {
			t.Fatalf("unknown request window = %+v", unknown)
		}
		unknownSpacing, err := snapshot.Spacing(999, WindowFilter{}, nil)
		if err != nil {
			return err
		}
		if unknownSpacing != 0 {
			t.Fatalf("unknown request spacing = %s", unknownSpacing)
		}
		return nil
	}); err != nil {
		t.Fatalf("View: %v", err)
	}
}

func TestReadRejectsInvalidFiltersCursorsAndCorruptStoredRounds(t *testing.T) {
	writer, reader, _ := readTestOpenPair(t)
	if err := reader.View(context.Background(), func(snapshot *Snapshot) error {
		if _, err := snapshot.Rounds(1, RoundFilter{NewerThan: -1}); err == nil {
			t.Fatal("negative NewerThan was accepted")
		}
		if _, err := snapshot.Rounds(1, RoundFilter{After: &RoundCursor{started: math.NaN(), id: 1}}); err == nil {
			t.Fatal("non-finite round cursor was accepted")
		}
		if _, err := snapshot.Runs(1, false, &RunCursor{started: math.Inf(1), rowID: 1}, 1); err == nil {
			t.Fatal("non-finite run cursor was accepted")
		}
		invalidTime := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		if _, err := snapshot.Window(1, WindowFilter{Since: &invalidTime}); err == nil {
			t.Fatal("unrepresentable window time was accepted")
		}
		if _, err := snapshot.Rounds(999, RoundFilter{}); err != nil {
			t.Fatalf("missing request should be empty: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("View: %v", err)
	}
	readTestExec(t, writer, `UPDATE request SET hosts=? WHERE id=?`, `[1]`, 1)
	if err := reader.View(context.Background(), func(snapshot *Snapshot) error {
		_, err := snapshot.Rounds(1, RoundFilter{Limit: 1})
		if err == nil || !strings.Contains(err.Error(), "stored request is invalid") {
			t.Fatalf("malformed stored host list error = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("View after malformed request: %v", err)
	}
	readTestExec(t, writer, `UPDATE request SET hosts=? WHERE id=?`, `["api.example.com","db.example.com","cache.example.com","queue.example.com","edge.example.com","worker.example.com","legacy.example.com"]`, 1)
	readTestExec(t, writer, `UPDATE round SET observations=? WHERE id=?`, "bad", 1)
	if err := reader.View(context.Background(), func(snapshot *Snapshot) error {
		_, err := snapshot.Rounds(1, RoundFilter{Limit: 1})
		if err == nil || !strings.Contains(err.Error(), "invalid stored round") {
			t.Fatalf("corrupt observations error = %v, want validation failure", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("View after corruption: %v", err)
	}
}

func TestRoundPageLimitAdaptsToHostCount(t *testing.T) {
	cases := []struct{ limit, hosts, want int }{
		{0, 0, 500},
		{200, 1, 200},
		{500, 256, 256},
		{500, 1000, 65},
		{1, 100000, 1},
	}
	for _, test := range cases {
		if got := roundPageLimit(test.limit, test.hosts); got != test.want {
			t.Errorf("roundPageLimit(%d,%d)=%d, want %d", test.limit, test.hosts, got, test.want)
		}
	}
}

func TestReadRejectsExcessRoundDetailsBeforeDecodingThem(t *testing.T) {
	writer, reader, _ := readTestOpenPair(t)
	err := writer.withTx(context.Background(), true, func(conn *sql.Conn) error {
		for i := 0; i < 1000; i++ {
			host := fmt.Sprintf("z-extra-%04d.example", i)
			tcp := any(nil)
			if i == 0 {
				tcp = "not-json"
			}
			if _, err := conn.ExecContext(context.Background(), `INSERT INTO result(round_id,host,address,t,result,tcp) VALUES(?,?,?,?,?,?)`, 1, host, "192.0.2.250", 1700000000.5, "extra", tcp); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = reader.View(context.Background(), func(snapshot *Snapshot) error {
		_, err := snapshot.Rounds(1, RoundFilter{Limit: 1})
		if err == nil || !strings.Contains(err.Error(), "unexpected or unseen result host") {
			t.Fatalf("excess result details error = %v, want early host membership error", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("View: %v", err)
	}
}

func TestReadRejectsNullNewestRunIdentifier(t *testing.T) {
	writer, reader, _ := readTestOpenPair(t)
	readTestExec(t, writer, `UPDATE run SET id=NULL WHERE id=?`, "fixture-running-zero-round")
	err := reader.View(context.Background(), func(snapshot *Snapshot) error {
		_, err := snapshot.Requests(nil, 10)
		if err == nil || !strings.Contains(err.Error(), "newest run") {
			t.Fatalf("NULL newest run ID error = %v, want malformed newest run", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("View: %v", err)
	}
}

func TestRequestSummariesRejectMalformedStoredHostsAndAcceptEmptyArray(t *testing.T) {
	for _, test := range []struct {
		name  string
		hosts string
	}{
		{name: "malformed-json", hosts: `not-json`},
		{name: "null-array", hosts: `null`},
		{name: "null-element", hosts: `[null]`},
		{name: "wrong-element-type", hosts: `[1]`},
		{name: "duplicate-host", hosts: `["x","x"]`},
		{name: "empty-host", hosts: `[""]`},
		{name: "invalid-utf8", hosts: "[\"" + string([]byte{0xff}) + "\"]"},
		{name: "unpaired-surrogate", hosts: `["\ud800"]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer, reader, _ := readTestOpenPair(t)
			readTestExec(t, writer, `UPDATE request SET hosts=? WHERE id=?`, test.hosts, 1)
			err := reader.View(context.Background(), func(snapshot *Snapshot) error {
				_, err := snapshot.Requests(nil, 10)
				if err == nil {
					t.Fatalf("request summary accepted host JSON %q: %v", test.hosts, err)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("View: %v", err)
			}
			if got := readTestRequestHosts(t, writer, 1); got != test.hosts {
				t.Fatalf("reader mutated stored host JSON to %q", got)
			}
		})
	}
	t.Run("valid-empty-array", func(t *testing.T) {
		writer, reader, _ := readTestOpenPair(t)
		readTestExec(t, writer, `UPDATE request SET hosts=? WHERE id=?`, `[]`, 1)
		if err := reader.View(context.Background(), func(snapshot *Snapshot) error {
			page, err := snapshot.Requests(nil, 10)
			if err != nil {
				return err
			}
			if len(page.Items) != 1 || page.Items[0].ID != 1 {
				t.Fatalf("valid empty-host summary = %+v", page.Items)
			}
			return nil
		}); err != nil {
			t.Fatalf("View valid empty array: %v", err)
		}
		if got := readTestRequestHosts(t, writer, 1); got != `[]` {
			t.Fatalf("readers changed valid empty hosts: %q", got)
		}
	})
}

func readTestRequestHosts(t *testing.T, store *Store, id int64) string {
	t.Helper()
	var hosts string
	err := store.withTx(context.Background(), false, func(conn *sql.Conn) error {
		return conn.QueryRowContext(context.Background(), `SELECT hosts FROM request WHERE id=?`, id).Scan(&hosts)
	})
	if err != nil {
		t.Fatal(err)
	}
	return hosts
}

func TestSpacingMatchesExpectedSpacingForGeneratedSamples(t *testing.T) {
	cases := []struct {
		name   string
		starts []float64
		filter WindowFilter
		extra  *float64
	}{
		{name: "ties-and-zero-gaps", starts: []float64{0, 1, 1, 3, 4}, filter: WindowFilter{Since: readTestPtrTime(time.Unix(0, 0)), Before: readTestPtrTime(time.Unix(100, 0))}},
		{name: "all-zero-gaps", starts: []float64{0, 0, 0}, filter: WindowFilter{Since: readTestPtrTime(time.Unix(0, 0)), Before: readTestPtrTime(time.Unix(100, 0))}},
		{name: "too-few", starts: []float64{0, 10}, filter: WindowFilter{Since: readTestPtrTime(time.Unix(0, 0)), Before: readTestPtrTime(time.Unix(100, 0))}},
		{name: "filtered-window", starts: []float64{0, 1, 1, 3, 4}, filter: WindowFilter{Since: readTestPtrTime(time.Unix(1, 0)), Before: readTestPtrTime(time.Unix(4, 0))}},
		{name: "extra-start", starts: []float64{0, 10}, filter: WindowFilter{Since: readTestPtrTime(time.Unix(0, 0)), Before: readTestPtrTime(time.Unix(100, 0))}, extra: readTestPtrFloat(20)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			writer, reader, _ := readTestOpenPair(t)
			err := writer.withTx(context.Background(), true, func(conn *sql.Conn) error {
				for _, started := range test.starts {
					if _, err := conn.ExecContext(context.Background(), `INSERT INTO round(run_id,started,observations,answered) VALUES(?,?,?,?)`, "fixture-finished", started, "???????", 0); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			var starts []time.Time
			for _, seconds := range test.starts {
				if test.filter.Since != nil && seconds < float64(test.filter.Since.Unix()) {
					continue
				}
				if test.filter.Before != nil && seconds >= float64(test.filter.Before.Unix()) {
					continue
				}
				starts = append(starts, readTestTime(t, seconds))
			}
			if test.extra != nil {
				starts = append(starts, readTestTime(t, *test.extra))
			}
			want := monitor.ExpectedSpacing(starts)
			err = reader.View(context.Background(), func(snapshot *Snapshot) error {
				var extra *time.Time
				if test.extra != nil {
					value := readTestTime(t, *test.extra)
					extra = &value
				}
				got, err := snapshot.Spacing(1, test.filter, extra)
				if err != nil {
					return err
				}
				if got != want {
					t.Fatalf("Spacing = %s, ExpectedSpacing = %s", got, want)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("View: %v", err)
			}
		})
	}
}

func TestRoundCursorRetainsSubNanosecondSQLiteKey(t *testing.T) {
	writer, reader, _ := readTestOpenPair(t)
	first := 0.1
	second := math.Nextafter(first, math.Inf(1))
	err := writer.withTx(context.Background(), true, func(conn *sql.Conn) error {
		for _, started := range []float64{first, second} {
			if _, err := conn.ExecContext(context.Background(), `INSERT INTO round(run_id,started,observations,answered) VALUES(?,?,?,?)`, "fixture-finished", started, "???????", 0); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = reader.View(context.Background(), func(snapshot *Snapshot) error {
		page, err := snapshot.Rounds(1, RoundFilter{Limit: 1})
		if err != nil {
			return err
		}
		if len(page.Items) != 1 || page.Next == nil || page.Next.started != first {
			t.Fatalf("first raw cursor = %+v, want %.17g", page.Next, first)
		}
		next, err := snapshot.Rounds(1, RoundFilter{After: page.Next, Limit: 1})
		if err != nil {
			return err
		}
		if len(next.Items) != 1 || next.Items[0].Cursor.started != second || !next.Items[0].Started.Equal(page.Items[0].Started) {
			t.Fatalf("sub-nanosecond raw keys not preserved: first=%+v next=%+v", page.Items, next.Items)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("View: %v", err)
	}
}

func TestSpacingRejectsMalformedStoredTimestamps(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
	}{
		{name: "year-10000", value: 253402300800.0},
		{name: "text-in-real-column", value: "not-a-time"},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer, reader, _ := readTestOpenPair(t)
			readTestExec(t, writer, `INSERT INTO round(run_id,started,observations,answered) VALUES(?,?,?,?)`, "fixture-finished", test.value, "???????", 0)
			err := reader.View(context.Background(), func(snapshot *Snapshot) error {
				_, err := snapshot.Spacing(1, WindowFilter{}, nil)
				if err == nil || !strings.Contains(err.Error(), "invalid stored round timestamp") {
					t.Fatalf("Spacing malformed timestamp error = %v", err)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("View: %v", err)
			}
		})
	}
}

func readTestPtrTime(value time.Time) *time.Time { return &value }
func readTestPtrFloat(value float64) *float64    { return &value }
