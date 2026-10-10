package history

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	interopFixtureDir = "testdata/python-source-v1"
	interopKey        = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	interopDriver     = "github.com/ncruces/go-sqlite3 v0.35.6"
)

type interopManifest struct {
	SchemaVersion int `json:"schema_version"`
	Request       struct {
		ID       int64    `json:"id"`
		Key      string   `json:"key"`
		Targets  []string `json:"targets"`
		TCPPorts []int    `json:"tcp_ports"`
		Hosts    []string `json:"hosts"`
	} `json:"request"`
	Runs []struct {
		ID           string   `json:"id"`
		RequestID    int64    `json:"request_id"`
		Started      float64  `json:"started"`
		Interval     *float64 `json:"interval"`
		Until        *float64 `json:"until"`
		Host         string   `json:"host"`
		PID          int64    `json:"pid"`
		Heartbeat    float64  `json:"heartbeat"`
		Ended        *float64 `json:"ended"`
		Status       string   `json:"status"`
		Rounds       int64    `json:"rounds"`
		RoundSeconds *float64 `json:"round_seconds"`
		Source       *string  `json:"source"`
	} `json:"runs"`
	Rounds []struct {
		ID           int64    `json:"id"`
		RunID        string   `json:"run_id"`
		Started      float64  `json:"started"`
		Interval     *float64 `json:"interval"`
		Observations string   `json:"observations"`
		Answered     int64    `json:"answered"`
		Rows         []struct {
			Host    string            `json:"host"`
			Address string            `json:"address"`
			Time    float64           `json:"time"`
			Result  string            `json:"result"`
			TCP     map[string]string `json:"tcp"`
		} `json:"rows"`
	} `json:"rounds"`
}

type interopProvenance struct {
	SourceCommit string `json:"source_commit"`
	FixtureSHA   string `json:"fixture_sha256"`
}

func TestPythonSourceFixtureSemantics(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	fixtureDir := filepath.Join("testdata", "python-source-v1")
	sourceDatabase := filepath.Join(fixtureDir, "cn-tool-schema1.db")
	manifestPath := filepath.Join(fixtureDir, "semantic-manifest.json")
	provenancePath := filepath.Join(fixtureDir, "provenance.json")

	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest interopManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode semantic manifest: %v", err)
	}
	provenanceBytes, err := os.ReadFile(provenancePath)
	if err != nil {
		t.Fatal(err)
	}
	var provenance interopProvenance
	if err := json.Unmarshal(provenanceBytes, &provenance); err != nil {
		t.Fatalf("decode source provenance: %v", err)
	}
	fixtureBytes, err := os.ReadFile(sourceDatabase)
	if err != nil {
		t.Fatal(err)
	}
	fixtureHash := sha256.Sum256(fixtureBytes)
	if got := hex.EncodeToString(fixtureHash[:]); got != provenance.FixtureSHA {
		t.Fatalf("fixture sha256=%s, provenance says %s", got, provenance.FixtureSHA)
	}
	if provenance.SourceCommit != "2e2f3c40ef6718486f1ca4477f9145029ec495fa" {
		t.Fatalf("fixture source commit=%s, want pinned cn-tool revision", provenance.SourceCommit)
	}
	if manifest.SchemaVersion != 1 || manifest.Request.Key != interopFixtureKey() {
		t.Fatalf("fixture schema/key mismatch: schema=%d key=%q", manifest.SchemaVersion, manifest.Request.Key)
	}
	// Opening a WAL database in read-only mode can still create -shm/-wal
	// sidecars. Exercise the reader against a disposable copy so the checked-in
	// source fixture stays byte-for-byte immutable on every platform.
	database := filepath.Join(t.TempDir(), "cn-tool-schema1.db")
	if err := os.WriteFile(database, fixtureBytes, 0o600); err != nil {
		t.Fatalf("copy source fixture to temporary database: %v", err)
	}

	reader, err := OpenReader(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if reader == nil {
		t.Fatal("OpenReader returned nil for the source-created fixture")
	}
	err = reader.View(ctx, func(snapshot *Snapshot) error {
		info, err := snapshot.Request(manifest.Request.Key)
		if err != nil {
			return err
		}
		if info == nil {
			return errors.New("source-created request is missing")
		}
		if info.ID != manifest.Request.ID || info.Key != manifest.Request.Key ||
			!interopEqualStrings(info.Targets, manifest.Request.Targets) ||
			!interopEqualInts(info.TCPPorts, manifest.Request.TCPPorts) ||
			!interopEqualStrings(info.Hosts, manifest.Request.Hosts) {
			return fmt.Errorf("source request mismatch: %+v", info)
		}
		if info.Runs != int64(len(manifest.Runs)) || info.Rounds != int64(len(manifest.Rounds)) {
			return fmt.Errorf("source request counts are runs=%d rounds=%d, want %d/%d", info.Runs, info.Rounds, len(manifest.Runs), len(manifest.Rounds))
		}
		if len(manifest.Runs) == 0 || info.NewestRun == nil || info.NewestRun.ID != "fixture-running-zero-round" {
			return fmt.Errorf("source newest run mismatch: %+v", info.NewestRun)
		}

		runs, err := snapshot.Runs(info.ID, false, nil, 20)
		if err != nil {
			return err
		}
		if len(runs.Items) != len(manifest.Runs) {
			return fmt.Errorf("source runs count=%d, want %d", len(runs.Items), len(manifest.Runs))
		}
		for _, expected := range manifest.Runs {
			actual, ok := interopRunByID(runs.Items, expected.ID)
			if !ok {
				return fmt.Errorf("source run %q missing", expected.ID)
			}
			if actual.RequestID != expected.RequestID || !interopEqualTime(actual.Started, expected.Started) ||
				actual.Host != expected.Host || actual.PID != expected.PID ||
				!interopEqualTime(actual.Heartbeat, expected.Heartbeat) || actual.Status != expected.Status || actual.Rounds != expected.Rounds ||
				!interopEqualOptionalTime(actual.Until, expected.Until) || !interopEqualOptionalTime(actual.Ended, expected.Ended) ||
				!interopEqualOptionalString(actual.Source, expected.Source) || !interopEqualOptionalDuration(actual.Interval, expected.Interval) ||
				!interopEqualOptionalDuration(actual.RoundSeconds, expected.RoundSeconds) {
				return fmt.Errorf("source run %q mismatch: got %+v expected %+v", expected.ID, actual, expected)
			}
		}

		page, err := snapshot.Rounds(info.ID, RoundFilter{Limit: 20})
		if err != nil {
			return err
		}
		if len(page.Items) != len(manifest.Rounds) {
			return fmt.Errorf("source rounds count=%d, want %d", len(page.Items), len(manifest.Rounds))
		}
		for i, expected := range manifest.Rounds {
			actual := page.Items[i]
			if actual.ID != expected.ID || actual.RunID != expected.RunID || !interopEqualTime(actual.Started, expected.Started) ||
				actual.Observations != expected.Observations || actual.Answered != expected.Answered ||
				!interopEqualOptionalDuration(actual.Interval, expected.Interval) || len(actual.Rows) != len(expected.Rows) {
				return fmt.Errorf("source round %d mismatch: got %+v expected %+v", i, actual, expected)
			}
			for j, expectedRow := range expected.Rows {
				actualRow := actual.Rows[j]
				if actualRow.Host != expectedRow.Host || actualRow.Address != expectedRow.Address || actualRow.Result != expectedRow.Result ||
					!interopEqualTime(actualRow.Time, expectedRow.Time) || !interopEqualTCP(actualRow.TCP, expectedRow.TCP) {
					return fmt.Errorf("source round %d row %d mismatch: got %+v expected %+v", i, j, actualRow, expectedRow)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close temporary fixture reader: %v", err)
	}
	for _, sidecar := range []string{sourceDatabase + "-wal", sourceDatabase + "-shm"} {
		if _, err := os.Stat(sidecar); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("source fixture sidecar %q exists; fixture reads must use a temporary copy", sidecar)
		}
	}
	if got := sha256.Sum256(mustRead(t, sourceDatabase)); hex.EncodeToString(got[:]) != provenance.FixtureSHA {
		t.Fatal("read-only fixture access changed the database bytes")
	}
}

func TestNativeHistoryPythonGoInterop(t *testing.T) {
	if os.Getenv("CNMON_HISTORY_INTEROP") != "1" {
		t.Skip("set CNMON_HISTORY_INTEROP=1 in the required native interop workflow")
	}
	source := os.Getenv("CNMON_CN_TOOL_SOURCE")
	if source == "" {
		t.Fatal("CNMON_CN_TOOL_SOURCE must point to the pinned cn-tool source checkout")
	}
	python := os.Getenv("CNMON_PYTHON")
	if python == "" {
		python = "python3"
		if runtime.GOOS == "windows" {
			python = "python"
		}
	}
	if _, err := exec.LookPath(python); err != nil {
		t.Fatalf("find Python interpreter %q: %v", python, err)
	}
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate interop helper")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../.."))
	helper := filepath.Join(repoRoot, "scripts", "history-python-interop.py")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()

	facts, err := runInteropPython(ctx, t, python, helper, source, "report", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("CNMON_HISTORY_INTEROP_ENV goos=%s goarch=%s go=%s cgo_enabled=%s driver=%s source_facts=%s", runtime.GOOS, runtime.GOARCH, runtime.Version(), os.Getenv("CGO_ENABLED"), interopDriver, facts)
	emitInteropNotice("runtime", fmt.Sprintf("goos=%s goarch=%s go=%s cgo_enabled=%s driver=%s source=%s", runtime.GOOS, runtime.GOARCH, runtime.Version(), os.Getenv("CGO_ENABLED"), interopDriver, facts))

	firstDB := filepath.Join(t.TempDir(), "python-created.db")
	if _, err := runInteropPython(ctx, t, python, helper, source, "create", firstDB, nil); err != nil {
		t.Fatal(err)
	}
	verifyInteropStore(t, firstDB, []string{"python-created"})
	goAppendInterop(t, firstDB, "go-appended", 1800000002.125)
	if _, err := runInteropPython(ctx, t, python, helper, source, "verify", firstDB, []string{"python-created", "go-appended"}); err != nil {
		t.Fatalf("source Python could not verify Go append: %v", err)
	}
	verifyInteropStore(t, firstDB, []string{"python-created", "go-appended"})

	secondDB := filepath.Join(t.TempDir(), "go-created.db")
	goAppendInterop(t, secondDB, "go-created", 1800000100.125)
	if _, err := runInteropPython(ctx, t, python, helper, source, "verify", secondDB, []string{"go-created"}); err != nil {
		t.Fatalf("source Python could not read Go-created history: %v", err)
	}
	if _, err := runInteropPython(ctx, t, python, helper, source, "append", secondDB, []string{"python-appended"}); err != nil {
		t.Fatalf("source Python could not append to Go-created history: %v", err)
	}
	verifyInteropStore(t, secondDB, []string{"go-created", "python-appended"})
	t.Log("CNMON_HISTORY_INTEROP_RESULT python-go-python=pass go-python-go=pass")
	emitInteropNotice("bidirectional", "python-to-go-to-python=pass go-to-python-to-go=pass")
}

func TestNativeHistoryConcurrentPythonGoWALAndPrune(t *testing.T) {
	python, helper, source := interopNativeInputs(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()

	database := filepath.Join(t.TempDir(), "concurrent.db")
	if _, err := runInteropPython(ctx, t, python, helper, source, "create", database, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := runInteropPythonArgs(ctx, t, python, helper, source, "append", database, []string{"--run-id", "old-prunable", "--started", "1700000000.125"}); err != nil {
		t.Fatal(err)
	}

	readyFile := filepath.Join(t.TempDir(), "python-writer-ready")
	startFile := filepath.Join(t.TempDir(), "python-writer-start")
	childCtx, childCancel := context.WithTimeout(ctx, 60*time.Second)
	defer childCancel()
	child := startInteropPython(t, childCtx, python, helper, source, "append-many", database, "--count", "8", "--ready-file", readyFile, "--start-file", startFile)
	if err := waitInteropFile(ctx, readyFile); err != nil {
		t.Fatalf("source Python concurrent writer did not become ready: %v; stdout=%s stderr=%s", err, child.stdout.String(), child.stderr.String())
	}

	readerCtx, readerCancel := context.WithTimeout(ctx, 60*time.Second)
	defer readerCancel()
	readerErr := make(chan error, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		readerErr <- readInteropWhileWriting(readerCtx, database, 24)
	}()
	t.Cleanup(func() {
		readerCancel()
		select {
		case <-readerDone:
		case <-time.After(3 * time.Second):
			t.Errorf("Go reader goroutine did not stop during bounded cleanup")
		}
	})
	if err := os.WriteFile(startFile, []byte("start\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for index := 0; index < 8; index++ {
		runID := fmt.Sprintf("go-concurrent-%03d", index)
		goAppendInterop(t, database, runID, 1800000300.125+float64(index*2))
	}
	select {
	case err := <-readerErr:
		if err != nil {
			t.Fatalf("Go reader during source Python/Go WAL writes: %v", err)
		}
	case <-readerCtx.Done():
		t.Fatalf("Go reader during concurrent WAL writes exceeded its deadline: %v", readerCtx.Err())
	}
	if err := waitInteropChild(ctx, child); err != nil {
		t.Fatalf("source Python concurrent writer failed: %v; stdout=%s stderr=%s", err, child.stdout.String(), child.stderr.String())
	}

	days := big.NewInt(30)
	pruneCtx, pruneCancel := interopTestContext(t)
	deleted, err := openAndPruneInterop(pruneCtx, database, interopTime(1800005000.125), days)
	pruneCancel()
	if err != nil {
		t.Fatalf("Go prune after concurrent WAL writes: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("Go prune deleted %d old rounds, want 1", deleted)
	}

	if _, err := runInteropPythonArgs(ctx, t, python, helper, source, "verify", database,
		[]string{"--expect-absent-run", "old-prunable"}); err != nil {
		t.Fatalf("source Python could not verify that Go pruning removed the old run: %v", err)
	}
	goIDs := make([]string, 0, 8)
	pythonIDs := make([]string, 0, 8)
	for index := 0; index < 8; index++ {
		goIDs = append(goIDs, fmt.Sprintf("go-concurrent-%03d", index))
		pythonIDs = append(pythonIDs, fmt.Sprintf("python-concurrent-%03d", index))
	}
	expectedIDs := append(append([]string{"python-created"}, goIDs...), pythonIDs...)
	if _, err := runInteropPython(ctx, t, python, helper, source, "verify", database, expectedIDs); err != nil {
		t.Fatalf("source Python cannot read all committed Go and Python writes: %v", err)
	}
	verifyInteropStore(t, database, expectedIDs)
	t.Log("CNMON_HISTORY_INTEROP_CONCURRENT python-reader=pass go-reader=pass source-writer=pass go-writer=pass prune=pass")
	emitInteropNotice("concurrent WAL", "python-reader=pass go-reader=pass source-writer=pass go-writer=pass prune=pass")
}

func TestNativeHistoryKilledPythonWriterRecovers(t *testing.T) {
	python, helper, source := interopNativeInputs(t)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	database := filepath.Join(t.TempDir(), "killed-writer.db")
	if _, err := runInteropPython(ctx, t, python, helper, source, "create", database, nil); err != nil {
		t.Fatal(err)
	}
	readyFile := filepath.Join(t.TempDir(), "writer-transaction-open")
	childCtx, childCancel := context.WithTimeout(ctx, 30*time.Second)
	defer childCancel()
	child := startInteropPython(t, childCtx, python, helper, source, "hold-transaction", database, "--run-id", "python-created", "--ready-file", readyFile)
	if err := waitInteropFile(ctx, readyFile); err != nil {
		t.Fatalf("source writer did not enter its uncommitted transaction: %v; stdout=%s stderr=%s", err, child.stdout.String(), child.stderr.String())
	}
	if err := child.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill source writer with open transaction: %v", err)
	}
	if err := waitInteropChild(ctx, child); err == nil {
		t.Fatal("source writer exited successfully even though the test killed its open transaction")
	}

	writerCtx, writerCancel := interopTestContext(t)
	writer, err := OpenWriter(writerCtx, database)
	writerCancel()
	if err != nil {
		t.Fatalf("reopen database after killed writer: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close recovered writer: %v", err)
	}
	verifyInteropStore(t, database, []string{"python-created"})
	if _, err := runInteropPythonArgs(ctx, t, python, helper, source, "verify", database,
		[]string{"--expect-run", "python-created", "--expect-status", "python-created=finished"}); err != nil {
		t.Fatalf("source Python could not verify transaction rollback after writer kill: %v", err)
	}
	t.Log("CNMON_HISTORY_INTEROP_KILLED_WRITER recovery=pass source-status=finished")
	emitInteropNotice("killed writer", "rollback-recovery=pass source-status=finished")
}

func TestNativeHistoryKilledGoWriterRecovers(t *testing.T) {
	python, helper, source := interopNativeInputs(t)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	database := filepath.Join(t.TempDir(), "killed-go-writer.db")
	if _, err := runInteropPython(ctx, t, python, helper, source, "create", database, nil); err != nil {
		t.Fatal(err)
	}
	goAppendInterop(t, database, "stable-before-go-crash", 1800000500.125)
	readyFile := filepath.Join(t.TempDir(), "go-writer-transaction-open")
	childCtx, childCancel := context.WithTimeout(ctx, 30*time.Second)
	defer childCancel()
	child := startGoCrashWriter(t, childCtx, database, "stable-before-go-crash", readyFile)
	if err := waitInteropFile(ctx, readyFile); err != nil {
		t.Fatalf("Go writer did not signal after writing its uncommitted round header and detail: %v; stdout=%s stderr=%s", err, child.stdout.String(), child.stderr.String())
	}
	if err := child.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill Go writer with open transaction: %v", err)
	}
	if err := waitInteropChild(ctx, child.interopChild); err == nil {
		t.Fatal("Go writer exited successfully even though the test killed its open transaction")
	}

	expectedRuns := []string{"python-created", "stable-before-go-crash"}
	verifyInteropStore(t, database, expectedRuns)
	if _, err := runInteropPythonArgs(ctx, t, python, helper, source, "verify", database,
		[]string{"--expect-run", "python-created", "--expect-run", "stable-before-go-crash"}); err != nil {
		t.Fatalf("source Python could not verify rollback after killing the Go writer: %v", err)
	}
	t.Log("CNMON_HISTORY_INTEROP_KILLED_GO_WRITER recovery=pass committed-round=preserved partial-round=absent")
	emitInteropNotice("killed Go writer", "rollback-recovery=pass committed-round=preserved partial-round=absent")
}

func TestNativeHistoryHeldGoSnapshotAcrossPythonPrune(t *testing.T) {
	python, helper, source := interopNativeInputs(t)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	database := filepath.Join(t.TempDir(), "go-snapshot-python-prune.db")
	if _, err := runInteropPython(ctx, t, python, helper, source, "create", database, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := runInteropPythonArgs(ctx, t, python, helper, source, "append", database, []string{"--run-id", "old-prunable", "--started", "1700000000.125"}); err != nil {
		t.Fatal(err)
	}

	reader, err := OpenReader(ctx, database)
	if err != nil || reader == nil {
		t.Fatalf("open Go snapshot reader: reader=%v error=%v", reader, err)
	}
	defer reader.Close()
	ready, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	viewDone := make(chan error, 1)
	viewExited := make(chan struct{})
	go func() {
		defer close(viewExited)
		viewDone <- reader.View(ctx, func(snapshot *Snapshot) error {
			before, err := interopSnapshotState(snapshot)
			if err != nil {
				return err
			}
			if !equalInteropIDs(before.runs, []string{"old-prunable", "python-created"}) || len(before.rounds) != 2 {
				return fmt.Errorf("unexpected initial Go snapshot before Python prune: runs=%v rounds=%v", before.runs, before.rounds)
			}
			close(ready)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			after, err := interopSnapshotState(snapshot)
			if err != nil {
				return err
			}
			if !equalInteropIDs(after.runs, before.runs) || !equalInteropIDs(after.rounds, before.rounds) {
				return fmt.Errorf("Go snapshot changed across Python commit/prune: before=%+v after=%+v", before, after)
			}
			return nil
		})
	}()
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		select {
		case <-viewExited:
		case <-time.After(3 * time.Second):
			t.Errorf("Go snapshot reader did not stop during bounded cleanup")
		}
	})
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatalf("Go snapshot did not establish its read view: %v", ctx.Err())
	}
	if _, err := runInteropPython(ctx, t, python, helper, source, "append-prune", database, nil); err != nil {
		releaseOnce.Do(func() { close(release) })
		t.Fatalf("source Python append/prune while Go snapshot is held: %v", err)
	}
	walBytes, err := interopWALBytes(database)
	if err != nil {
		releaseOnce.Do(func() { close(release) })
		t.Fatalf("measure WAL while Go snapshot is held: %v", err)
	}
	if walBytes <= 0 || walBytes > 1<<20 {
		releaseOnce.Do(func() { close(release) })
		t.Fatalf("held Go snapshot WAL size=%d bytes, want 1..1048576", walBytes)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-viewDone:
		if err != nil {
			t.Fatalf("Go snapshot across source Python commit/prune: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("Go snapshot did not close after release: %v", ctx.Err())
	}
	expected := []string{"python-created", "python-after-prune"}
	verifyInteropStore(t, database, expected)
	if _, err := runInteropPythonArgs(ctx, t, python, helper, source, "verify", database,
		[]string{"--expect-run", "python-created", "--expect-run", "python-after-prune", "--expect-absent-run", "old-prunable"}); err != nil {
		t.Fatalf("source Python could not verify fresh view after its prune: %v", err)
	}
	t.Logf("CNMON_HISTORY_INTEROP_GO_SNAPSHOT held-view=stable python-commit=visible python-prune=visible next-view=updated wal_bytes=%d", walBytes)
	emitInteropNotice("Go snapshot across Python prune", fmt.Sprintf("held-view=stable python-commit=visible python-prune=visible next-view=updated wal_bytes=%d", walBytes))
}

func TestNativeHistoryHeldPythonSnapshotAcrossGoPrune(t *testing.T) {
	python, helper, source := interopNativeInputs(t)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	database := filepath.Join(t.TempDir(), "python-snapshot-go-prune.db")
	if _, err := runInteropPython(ctx, t, python, helper, source, "create", database, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := runInteropPythonArgs(ctx, t, python, helper, source, "append", database, []string{"--run-id", "old-prunable", "--started", "1700000000.125"}); err != nil {
		t.Fatal(err)
	}
	readyFile := filepath.Join(t.TempDir(), "python-snapshot-ready")
	releaseFile := filepath.Join(t.TempDir(), "python-snapshot-release")
	childCtx, childCancel := context.WithTimeout(ctx, 45*time.Second)
	defer childCancel()
	child := startInteropPython(t, childCtx, python, helper, source, "hold-snapshot", database,
		"--ready-file", readyFile, "--release-file", releaseFile)
	if err := waitInteropFile(ctx, readyFile); err != nil {
		t.Fatalf("source Python read snapshot did not become ready: %v; stdout=%s stderr=%s", err, child.stdout.String(), child.stderr.String())
	}
	pruneCtx, pruneCancel := interopTestContext(t)
	deleted, err := openAndPruneInterop(pruneCtx, database, interopTime(1800005000.125), big.NewInt(30))
	pruneCancel()
	if err != nil || deleted != 1 {
		t.Fatalf("Go prune while Python snapshot is held deleted=%d error=%v, want 1", deleted, err)
	}
	goAppendInterop(t, database, "go-after-prune", 1800000400.125)
	walBytes, err := interopWALBytes(database)
	if err != nil {
		t.Fatalf("measure WAL while Python snapshot is held: %v", err)
	}
	if walBytes <= 0 || walBytes > 1<<20 {
		t.Fatalf("held Python snapshot WAL size=%d bytes, want 1..1048576", walBytes)
	}
	if err := os.WriteFile(releaseFile, []byte("release\n"), 0o600); err != nil {
		t.Fatalf("release source Python read snapshot: %v", err)
	}
	if err := waitInteropChild(ctx, child); err != nil {
		t.Fatalf("source Python snapshot changed across Go prune: %v; stdout=%s stderr=%s", err, child.stdout.String(), child.stderr.String())
	}
	expectedRuns := []string{"python-created", "go-after-prune"}
	verifyInteropStore(t, database, expectedRuns)
	if _, err := runInteropPythonArgs(ctx, t, python, helper, source, "verify", database,
		[]string{"--expect-run", "python-created", "--expect-run", "go-after-prune", "--expect-absent-run", "old-prunable"}); err != nil {
		t.Fatalf("source Python could not verify fresh view after Go prune: %v", err)
	}
	t.Logf("CNMON_HISTORY_INTEROP_PYTHON_SNAPSHOT held-view=stable go-commit=visible go-prune=visible next-view=updated wal_bytes=%d", walBytes)
	emitInteropNotice("Python snapshot across Go prune", fmt.Sprintf("held-view=stable go-commit=visible go-prune=visible next-view=updated wal_bytes=%d", walBytes))
}

type interopSnapshotIDs struct {
	runs   []string
	rounds []int64
}

func interopSnapshotState(snapshot *Snapshot) (interopSnapshotIDs, error) {
	request, err := snapshot.Request(interopKey)
	if err != nil {
		return interopSnapshotIDs{}, err
	}
	if request == nil {
		return interopSnapshotIDs{}, errors.New("snapshot request is missing")
	}
	runs, err := snapshot.Runs(request.ID, false, nil, 100)
	if err != nil {
		return interopSnapshotIDs{}, err
	}
	rounds, err := snapshot.Rounds(request.ID, RoundFilter{Limit: 100})
	if err != nil {
		return interopSnapshotIDs{}, err
	}
	state := interopSnapshotIDs{runs: make([]string, 0, len(runs.Items)), rounds: make([]int64, 0, len(rounds.Items))}
	for _, run := range runs.Items {
		state.runs = append(state.runs, run.ID)
	}
	for _, round := range rounds.Items {
		state.rounds = append(state.rounds, round.ID)
	}
	sort.Strings(state.runs)
	sort.Slice(state.rounds, func(i, j int) bool { return state.rounds[i] < state.rounds[j] })
	return state, nil
}

func equalInteropIDs[T comparable](left, right []T) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func interopWALBytes(database string) (int64, error) {
	info, err := os.Stat(database + "-wal")
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func TestInteropGoCrashWriterProcess(t *testing.T) {
	if os.Getenv("CNMON_HISTORY_GO_CRASH_CHILD") != "1" {
		return
	}
	database := os.Getenv("CNMON_HISTORY_GO_CRASH_DATABASE")
	readyFile := os.Getenv("CNMON_HISTORY_GO_CRASH_READY")
	runID := os.Getenv("CNMON_HISTORY_GO_CRASH_RUN")
	if database == "" || readyFile == "" || runID == "" {
		t.Fatal("Go crash child requires database, ready-file, and run ID")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	store, err := OpenWriter(ctx, database)
	if err != nil {
		t.Fatalf("open Go crash child writer: %v", err)
	}
	defer store.Close()
	err = store.withTx(ctx, true, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, `INSERT INTO round (run_id, started, observations, answered) VALUES (?, ?, ?, ?)`,
			runID, 1800000510.125, "CRASH-UNCOMMITTED", 1)
		if err != nil {
			return err
		}
		roundID, err := result.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO result (round_id, host, address, t, result, tcp) VALUES (?, ?, ?, ?, ?, NULL)`,
			roundID, "interop.example", "192.0.2.99", 1800000510.25, "CRASH-UNCOMMITTED"); err != nil {
			return err
		}
		if err := os.WriteFile(readyFile, []byte("header-and-detail-written\n"), 0o600); err != nil {
			return err
		}
		select {}
	})
	if err != nil {
		t.Fatalf("Go crash child transaction ended unexpectedly: %v", err)
	}
	t.Fatal("Go crash child transaction returned without being killed")
}

type goCrashChild struct {
	*interopChild
}

func startGoCrashWriter(t *testing.T, ctx context.Context, database, runID, readyFile string) *goCrashChild {
	t.Helper()
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("locate current Go test binary: %v", err)
	}
	cmd := exec.CommandContext(ctx, testBinary, "-test.run=^TestInteropGoCrashWriterProcess$", "-test.timeout=25s")
	cmd.Env = append(os.Environ(),
		"CNMON_HISTORY_GO_CRASH_CHILD=1",
		"CNMON_HISTORY_GO_CRASH_DATABASE="+database,
		"CNMON_HISTORY_GO_CRASH_READY="+readyFile,
		"CNMON_HISTORY_GO_CRASH_RUN="+runID,
	)
	child := &interopChild{cmd: cmd, done: make(chan error, 1), stdout: new(lockedBuffer), stderr: new(lockedBuffer)}
	cmd.Stdout, cmd.Stderr = child.stdout, child.stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start Go crash writer child: %v", err)
	}
	go func() { child.done <- cmd.Wait() }()
	t.Cleanup(func() {
		if child.waited {
			return
		}
		if child.cmd.Process != nil {
			_ = child.cmd.Process.Kill()
		}
		select {
		case <-child.done:
			child.waited = true
		case <-time.After(3 * time.Second):
			t.Errorf("Go crash writer child did not exit during bounded cleanup")
		}
	})
	return &goCrashChild{interopChild: child}
}

func runInteropPython(ctx context.Context, t *testing.T, python, helper, source, operation, database string, expectedRuns []string) (string, error) {
	t.Helper()
	extra := make([]string, 0, len(expectedRuns)*2)
	for _, runID := range expectedRuns {
		extra = append(extra, "--expect-run", runID)
	}
	return runInteropPythonArgs(ctx, t, python, helper, source, operation, database, extra)
}

func runInteropPythonArgs(ctx context.Context, t *testing.T, python, helper, source, operation, database string, extra []string) (string, error) {
	t.Helper()
	args := []string{helper, "--source", source, "--operation", operation}
	if database != "" {
		args = append(args, "--database", database)
	}
	args = append(args, extra...)
	cmd := exec.CommandContext(ctx, python, args...)
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	stdout, stderr := new(lockedBuffer), new(lockedBuffer)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("Python interop operation %s failed: %w; stderr=%s", operation, err, stderr.String())
	}
	if text := strings.TrimSpace(stdout.String()); text != "" {
		t.Log(text)
	}
	return strings.TrimSpace(stdout.String()), nil
}

type interopChild struct {
	cmd    *exec.Cmd
	done   chan error
	stdout *lockedBuffer
	stderr *lockedBuffer
	waited bool
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func startInteropPython(t *testing.T, ctx context.Context, python, helper, source, operation, database string, extra ...string) *interopChild {
	t.Helper()
	args := []string{helper, "--source", source, "--operation", operation, "--database", database}
	args = append(args, extra...)
	cmd := exec.CommandContext(ctx, python, args...)
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	child := &interopChild{cmd: cmd, done: make(chan error, 1), stdout: new(lockedBuffer), stderr: new(lockedBuffer)}
	cmd.Stdout, cmd.Stderr = child.stdout, child.stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start Python interop operation %s: %v", operation, err)
	}
	go func() { child.done <- cmd.Wait() }()
	t.Cleanup(func() {
		if child.waited {
			return
		}
		if child.cmd.Process != nil {
			_ = child.cmd.Process.Kill()
		}
		select {
		case <-child.done:
			child.waited = true
		case <-time.After(3 * time.Second):
			t.Errorf("Python interop child did not exit during bounded cleanup")
		}
	})
	return child
}

func waitInteropChild(ctx context.Context, child *interopChild) error {
	select {
	case err := <-child.done:
		child.waited = true
		return err
	case <-ctx.Done():
		if child.cmd.Process != nil {
			_ = child.cmd.Process.Kill()
		}
		select {
		case err := <-child.done:
			child.waited = true
			return fmt.Errorf("Python child exceeded its deadline: %w (exit %v)", ctx.Err(), err)
		case <-time.After(3 * time.Second):
			return fmt.Errorf("Python child exceeded its deadline and did not exit: %w", ctx.Err())
		}
	}
}

func waitInteropFile(ctx context.Context, path string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func interopNativeInputs(t *testing.T) (python, helper, source string) {
	t.Helper()
	if os.Getenv("CNMON_HISTORY_INTEROP") != "1" {
		t.Skip("set CNMON_HISTORY_INTEROP=1 in the required native interop workflow")
	}
	source = os.Getenv("CNMON_CN_TOOL_SOURCE")
	if source == "" {
		t.Fatal("CNMON_CN_TOOL_SOURCE must point to the pinned cn-tool source checkout")
	}
	python = os.Getenv("CNMON_PYTHON")
	if python == "" {
		python = "python3"
		if runtime.GOOS == "windows" {
			python = "python"
		}
	}
	if _, err := exec.LookPath(python); err != nil {
		t.Fatalf("find Python interpreter %q: %v", python, err)
	}
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate interop helper")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../.."))
	helper = filepath.Join(repoRoot, "scripts", "history-python-interop.py")
	return python, helper, source
}

func readInteropWhileWriting(ctx context.Context, database string, iterations int) error {
	reader, err := OpenReader(ctx, database)
	if err != nil {
		return err
	}
	if reader == nil {
		return errors.New("Go reader did not find the concurrent history database")
	}
	defer reader.Close()
	for iteration := 0; iteration < iterations; iteration++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := reader.View(ctx, func(snapshot *Snapshot) error {
			request, err := snapshot.Request(interopKey)
			if err != nil {
				return err
			}
			if request == nil {
				return errors.New("Go reader cannot see source-created request")
			}
			if _, err := snapshot.Runs(request.ID, false, nil, 100); err != nil {
				return err
			}
			page, err := snapshot.Rounds(request.ID, RoundFilter{Limit: 100})
			if err != nil {
				return err
			}
			for _, round := range page.Items {
				if int64(len(round.Rows)) != round.Answered || len(round.Rows) != 1 {
					return fmt.Errorf("Go reader observed incomplete committed round: %+v", round)
				}
			}
			return nil
		}); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	return nil
}

func openAndPruneInterop(ctx context.Context, database string, now time.Time, days *big.Int) (int64, error) {
	store, err := OpenWriter(ctx, database)
	if err != nil {
		return 0, err
	}
	defer store.Close()
	return store.Prune(ctx, now, days)
}

func goAppendInterop(t *testing.T, database, runID string, startedSeconds float64) {
	t.Helper()
	ctx, cancel := interopTestContext(t)
	defer cancel()
	store, err := OpenWriter(ctx, database)
	if err != nil {
		t.Fatalf("open Go writer for %s: %v", runID, err)
	}
	request := Request{RequestMeta: RequestMeta{Key: interopKey, Targets: []string{"interop.example"}, TCPPorts: []int{}}, Hosts: []string{"interop.example", "silent.example"}}
	started := interopTime(startedSeconds)
	_, err = store.BeginRun(ctx, request, RunStart{ID: runID, Source: interopString("198.51.100.44"), Started: started, Host: "interop-test-host", PID: 8765, Heartbeat: started.Add(125 * time.Millisecond)})
	if err != nil {
		_ = store.Close()
		t.Fatalf("Go BeginRun %s: %v", runID, err)
	}
	_, err = store.RecordRound(ctx, runID, RoundInput{Started: started.Add(250 * time.Millisecond), Observations: "!.", Rows: []ResultRow{{Host: "interop.example", Address: "192.0.2.44", Time: started.Add(375 * time.Millisecond), Result: "OK (2/2 replies)"}}})
	if err != nil {
		_ = store.Close()
		t.Fatalf("Go RecordRound %s: %v", runID, err)
	}
	if err := store.EndRun(ctx, runID, started.Add(500*time.Millisecond), "finished"); err != nil {
		_ = store.Close()
		t.Fatalf("Go EndRun %s: %v", runID, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close Go writer %s: %v", runID, err)
	}
}

func verifyInteropStore(t *testing.T, database string, expectedRunIDs []string) {
	t.Helper()
	ctx, cancel := interopTestContext(t)
	defer cancel()
	reader, err := OpenReader(ctx, database)
	if err != nil {
		t.Fatalf("open Go reader: %v", err)
	}
	if reader == nil {
		t.Fatal("Go reader returned nil for an existing interop database")
	}
	defer reader.Close()
	err = reader.View(ctx, func(snapshot *Snapshot) error {
		var sqliteVersion string
		if err := snapshot.conn.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&sqliteVersion); err != nil {
			return err
		}
		var pageCount, pageSize, freePages int64
		for pragma, destination := range map[string]*int64{
			"page_count": &pageCount, "page_size": &pageSize, "freelist_count": &freePages,
		} {
			if err := snapshot.conn.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(destination); err != nil {
				return err
			}
		}
		var journalMode string
		if err := snapshot.conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
			return err
		}
		if journalMode != "wal" || pageCount < 1 || pageSize < 1 || freePages < 0 {
			return fmt.Errorf("unexpected SQLite storage metrics: journal=%q pages=%d page_size=%d free=%d", journalMode, pageCount, pageSize, freePages)
		}
		info, err := snapshot.Request(interopKey)
		if err != nil {
			return err
		}
		if info == nil || info.Key != interopKey || !interopEqualStrings(info.Targets, []string{"interop.example"}) ||
			len(info.TCPPorts) != 0 || !interopEqualStrings(info.Hosts, []string{"interop.example", "silent.example"}) {
			return fmt.Errorf("Python/Go request mismatch: %+v", info)
		}
		runs, err := snapshot.Runs(info.ID, false, nil, 20)
		if err != nil {
			return err
		}
		seen := make(map[string]bool, len(runs.Items))
		for _, run := range runs.Items {
			seen[run.ID] = true
			if run.Status != "finished" && run.Status != "stopped" {
				return fmt.Errorf("unexpected interop run status for %s: %q", run.ID, run.Status)
			}
		}
		for _, runID := range expectedRunIDs {
			if !seen[runID] {
				return fmt.Errorf("Go reader cannot see committed run %q", runID)
			}
		}
		if len(seen) != len(expectedRunIDs) {
			return fmt.Errorf("Go reader found %d runs, want exactly %d", len(seen), len(expectedRunIDs))
		}
		page, err := snapshot.Rounds(info.ID, RoundFilter{Limit: 20})
		if err != nil {
			return err
		}
		if len(page.Items) != len(expectedRunIDs) {
			return fmt.Errorf("Go reader found %d rounds, want %d", len(page.Items), len(expectedRunIDs))
		}
		for _, round := range page.Items {
			if len(round.Rows) != 1 || round.Answered != 1 {
				return fmt.Errorf("Go reader found an incomplete committed round: %+v", round)
			}
		}
		t.Logf("CNMON_HISTORY_INTEROP_SQLITE version=%s journal=%s pages=%d page_size=%d freelist_pages=%d runs=%d rounds=%d",
			sqliteVersion, journalMode, pageCount, pageSize, freePages, len(runs.Items), len(page.Items))
		emitInteropNotice("SQLite pages", fmt.Sprintf("sqlite=%s journal=%s pages=%d page_size=%d freelist_pages=%d runs=%d rounds=%d",
			sqliteVersion, journalMode, pageCount, pageSize, freePages, len(runs.Items), len(page.Items)))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	t.Logf("CNMON_HISTORY_INTEROP_MEMORY heap_alloc_bytes=%d heap_sys_bytes=%d", memory.Alloc, memory.HeapSys)
	emitInteropNotice("Go heap sample", fmt.Sprintf("heap_alloc_bytes=%d heap_sys_bytes=%d", memory.Alloc, memory.HeapSys))
}

func emitInteropNotice(title, message string) {
	if os.Getenv("GITHUB_ACTIONS") == "" {
		return
	}
	message = strings.ReplaceAll(message, "%", "%25")
	message = strings.ReplaceAll(message, "\r", "%0D")
	message = strings.ReplaceAll(message, "\n", "%0A")
	fmt.Printf("::notice title=cn-mon history %s::%s\n", title, message)
}

func interopTestContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), 20*time.Second)
}

func interopFixtureKey() string {
	return "24156eb156b5bb9d83f75c5e4391db9736dee2ae690bd2c99e8154414d124fff"
}

func interopRunByID(runs []RunInfo, id string) (RunInfo, bool) {
	for _, run := range runs {
		if run.ID == id {
			return run, true
		}
	}
	return RunInfo{}, false
}

func interopEqualTime(got time.Time, expected float64) bool {
	want := interopTime(expected)
	return got.Equal(want)
}

func interopTime(seconds float64) time.Time {
	whole, fraction := math.Modf(seconds)
	return time.Unix(int64(whole), int64(math.Round(fraction*1e9))).UTC()
}

func interopEqualOptionalTime(got *time.Time, expected *float64) bool {
	if got == nil || expected == nil {
		return got == nil && expected == nil
	}
	return interopEqualTime(*got, *expected)
}

func interopEqualOptionalString(got *string, expected *string) bool {
	if got == nil || expected == nil {
		return got == nil && expected == nil
	}
	return *got == *expected
}

func interopEqualOptionalDuration(got *time.Duration, expected *float64) bool {
	if got == nil || expected == nil {
		return got == nil && expected == nil
	}
	want := time.Duration(math.Round(*expected * float64(time.Second)))
	return *got == want
}

func interopEqualStrings(got, expected []string) bool {
	if len(got) != len(expected) {
		return false
	}
	for i := range got {
		if got[i] != expected[i] {
			return false
		}
	}
	return true
}

func interopEqualInts(got, expected []int) bool {
	if len(got) != len(expected) {
		return false
	}
	for i := range got {
		if got[i] != expected[i] {
			return false
		}
	}
	return true
}

func interopEqualTCP(got map[int]string, expected map[string]string) bool {
	if got == nil || expected == nil {
		return got == nil && expected == nil
	}
	if len(got) != len(expected) {
		return false
	}
	for key, value := range expected {
		var port int
		if _, err := fmt.Sscanf(key, "%d", &port); err != nil || got[port] != value {
			return false
		}
	}
	return true
}

func interopString(value string) *string { return &value }

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
