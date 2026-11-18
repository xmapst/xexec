//go:build windows

package xexec

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeWindowsTree struct {
	children               map[uint32][]uint32
	created                map[uint32]uint64
	openErrors, killErrors map[uint32]error
	scanError              error
	opened, killed, closed []uint32
	beforeKill             func(uint32)
	beforeOpen             func(uint32)
	active                 map[uint32]bool
	peakHandles            int
}

func (f *fakeWindowsTree) ops() windowsTreeOps {
	return windowsTreeOps{
		cutoff:   func() uint64 { return 100 },
		snapshot: func() (map[uint32][]uint32, error) { return f.children, f.scanError },
		open: func(pid uint32) (*windowsTreeProcess, error) {
			if f.beforeOpen != nil {
				f.beforeOpen(pid)
			}
			f.opened = append(f.opened, pid)
			if err := f.openErrors[pid]; err != nil {
				return nil, err
			}
			if f.active == nil {
				f.active = make(map[uint32]bool)
			}
			f.active[pid] = true
			f.peakHandles = max(f.peakHandles, len(f.active))
			return &windowsTreeProcess{
				created: f.created[pid],
				terminate: func() error {
					if f.beforeKill != nil {
						f.beforeKill(pid)
					}
					f.killed = append(f.killed, pid)
					return f.killErrors[pid]
				},
				close: func() {
					f.closed = append(f.closed, pid)
					delete(f.active, pid)
				},
			}, nil
		},
	}
}

func TestWindowsNativeTreeInvalidPIDDoesNotCallAPI(t *testing.T) {
	pids := []int32{-1 << 31, -2, -1, 0, 4}
	for _, pid := range pids {
		if err := killWindowsTree(pid, windowsTreeOps{}); err == nil {
			t.Fatalf("invalid pid %d accepted", pid)
		}
	}
}

func TestWindowsNativeTreeCyclesDuplicatesAndTerminationOrder(t *testing.T) {
	f := &fakeWindowsTree{children: map[uint32][]uint32{
		10: {20, 20, 0, 4, 10, 30}, 20: {40}, 40: {10, 20}, 4: {99},
	}}
	f.beforeKill = func(pid uint32) {
		want := map[uint32][]uint32{
			40: {10, 20, 40}, 20: {10, 20}, 30: {10, 30}, 10: {10},
		}[pid]
		if len(f.active) != len(want) {
			t.Fatalf("terminate %d: active handles %v, want %v", pid, f.active, want)
		}
		for _, ancestor := range want {
			if !f.active[ancestor] {
				t.Fatalf("terminate %d without retained handle %d", pid, ancestor)
			}
		}
	}
	ops := f.ops()
	snapshot := ops.snapshot
	ops.snapshot = func() (map[uint32][]uint32, error) {
		if !reflect.DeepEqual(f.opened, []uint32{10}) {
			t.Fatalf("root not retained before snapshot: %v", f.opened)
		}
		return snapshot()
	}
	if err := killWindowsTree(10, ops); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.opened, []uint32{10, 20, 40, 30}) {
		t.Fatalf("opened: %v", f.opened)
	}
	if !reflect.DeepEqual(f.killed, []uint32{40, 20, 30, 10}) {
		t.Fatalf("kill order: %v", f.killed)
	}
	if !reflect.DeepEqual(f.closed, f.killed) || len(f.active) != 0 {
		t.Fatalf("handle cleanup: killed=%v closed=%v active=%v", f.killed, f.closed, f.active)
	}
}

func TestWindowsNativeTreePreservesErrorsAndContinues(t *testing.T) {
	openErr, childErr, rootErr := errors.New("open denied"), errors.New("child denied"), errors.New("root denied")
	f := &fakeWindowsTree{
		children:   map[uint32][]uint32{10: {20, 30, 40}},
		openErrors: map[uint32]error{20: openErr},
		killErrors: map[uint32]error{30: childErr, 10: rootErr},
	}
	err := killWindowsTree(10, f.ops())
	for _, want := range []error{openErr, childErr, rootErr} {
		if !errors.Is(err, want) {
			t.Fatalf("missing %v in %v", want, err)
		}
	}
	for _, pid := range []string{"20", "30", "10"} {
		if !strings.Contains(err.Error(), pid) {
			t.Fatalf("missing PID %s: %v", pid, err)
		}
	}
	if !reflect.DeepEqual(f.killed, []uint32{30, 40, 10}) {
		t.Fatalf("did not continue: %v", f.killed)
	}
	if !reflect.DeepEqual(f.closed, f.killed) || len(f.active) != 0 {
		t.Fatalf("error path leaked handles: closed=%v active=%v", f.closed, f.active)
	}
}

func TestWindowsNativeTreeSnapshotFailureStillTerminatesRoot(t *testing.T) {
	sentinel := errors.New("snapshot denied")
	f := &fakeWindowsTree{scanError: sentinel}
	err := killWindowsTree(10, f.ops())
	if !errors.Is(err, sentinel) {
		t.Fatalf("snapshot error: %v", err)
	}
	if got, ok := errors.AsType[*Error](err); !ok || got.Op != "snapshot process tree" || got.PID != 10 || got.Err != sentinel {
		t.Fatalf("snapshot error context lost: %#v", err)
	}
	if !reflect.DeepEqual(f.killed, []uint32{10}) || !reflect.DeepEqual(f.closed, []uint32{10}) {
		t.Fatalf("root not cleaned: %+v", f)
	}
}

func TestWindowsNativeTreeRootOpenFailureStops(t *testing.T) {
	sentinel := errors.New("root open denied")
	f := &fakeWindowsTree{openErrors: map[uint32]error{10: sentinel}}
	ops := f.ops()
	ops.snapshot = func() (map[uint32][]uint32, error) { t.Fatal("snapshot after root open failed"); return nil, nil }
	err := killWindowsTree(10, ops)
	if !errors.Is(err, sentinel) {
		t.Fatalf("open error: %v", err)
	}
	if got, ok := errors.AsType[*Error](err); !ok || got.Op != "open root process" || got.PID != 10 || got.Err != sentinel {
		t.Fatalf("root open error context lost: %#v", err)
	}
	if len(f.killed) != 0 || len(f.closed) != 0 {
		t.Fatalf("unexpected cleanup: %+v", f)
	}
}

func TestWindowsNativeTreeRejectsStaleAndReusedPIDs(t *testing.T) {
	f := &fakeWindowsTree{
		children: map[uint32][]uint32{10: {20, 30, 40}, 20: {50}, 30: {60}},
		created:  map[uint32]uint64{10: 10, 20: 9, 30: 101, 40: 11},
	}
	err := killWindowsTree(10, f.ops())
	if err == nil {
		t.Fatal("identity uncertainty was discarded")
	}
	if detail, ok := errors.AsType[*Error](err); !ok || detail.PID != 20 || detail.Value != "parent_pid=10 parent_created=10 child_created=9 cutoff=100" {
		t.Fatalf("stale-parent diagnostics lost: %v", err)
	}
	if !strings.Contains(err.Error(), "parent_pid=10 parent_created=10 child_created=101 cutoff=100") {
		t.Fatalf("post-cutoff diagnostics lost: %v", err)
	}
	if !reflect.DeepEqual(f.killed, []uint32{40, 10}) {
		t.Fatalf("unsafe targets terminated: %v", f.killed)
	}
	if !reflect.DeepEqual(f.opened, []uint32{10, 20, 30, 40}) {
		t.Fatalf("followed stale parent: %v", f.opened)
	}
	if !reflect.DeepEqual(f.closed, []uint32{20, 30, 40, 10}) || len(f.active) != 0 {
		t.Fatalf("rejected handles leaked: closed=%v active=%v", f.closed, f.active)
	}
	if f.peakHandles != 2 {
		t.Fatalf("rejected handles retained across siblings: peak=%d", f.peakHandles)
	}
}

func TestWindowsNativeTreeWideTreeBoundsHandlesByDepth(t *testing.T) {
	f := &fakeWindowsTree{children: make(map[uint32][]uint32)}
	const branches = 200
	for pid := uint32(20); pid < 20+branches; pid++ {
		f.children[10] = append(f.children[10], pid)
		f.children[pid] = []uint32{pid + 1000}
	}
	f.beforeOpen = func(pid uint32) {
		if pid == 10 {
			return
		}
		if !f.active[10] {
			t.Fatal("root handle closed during traversal")
		}
		if pid < 1000 {
			if len(f.active) != 1 {
				t.Fatalf("opening sibling %d before closing previous branch: %v", pid, f.active)
			}
		} else if len(f.active) != 2 || !f.active[pid-1000] {
			t.Fatalf("opening leaf %d without its ancestor chain: %v", pid, f.active)
		}
	}
	if err := killWindowsTree(10, f.ops()); err != nil {
		t.Fatal(err)
	}
	if f.peakHandles != 3 {
		t.Fatalf("peak handles = %d, want tree depth 3", f.peakHandles)
	}
	if len(f.killed) != 1+2*branches || !reflect.DeepEqual(f.closed, f.killed) || len(f.active) != 0 {
		t.Fatalf("incomplete cleanup: killed=%d closed=%d active=%v", len(f.killed), len(f.closed), f.active)
	}
}

func TestWindowsNativeTreeDeepChainClosesInPostorder(t *testing.T) {
	f := &fakeWindowsTree{children: make(map[uint32][]uint32)}
	const depth = 256
	var want []uint32
	for i := uint32(0); i < depth; i++ {
		pid := 10 + i
		if i+1 < depth {
			f.children[pid] = []uint32{pid + 1}
		}
		want = append(want, 10+depth-1-i)
	}
	f.beforeKill = func(pid uint32) {
		if len(f.active) != int(pid-10+1) {
			t.Fatalf("terminate %d: descendant handles still retained: %v", pid, f.active)
		}
		for ancestor := uint32(10); ancestor <= pid; ancestor++ {
			if !f.active[ancestor] {
				t.Fatalf("terminate %d: ancestor %d closed early", pid, ancestor)
			}
		}
	}
	if err := killWindowsTree(10, f.ops()); err != nil {
		t.Fatal(err)
	}
	if f.peakHandles != depth || !reflect.DeepEqual(f.killed, want) || !reflect.DeepEqual(f.closed, want) || len(f.active) != 0 {
		t.Fatalf("postorder cleanup: peak=%d killed=%v closed=%v active=%v", f.peakHandles, f.killed, f.closed, f.active)
	}
}
