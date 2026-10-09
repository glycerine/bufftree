package bench

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"testing"
)

const memoryEntries = 100_000
const memoryWorkerEnv = "BUFFTREE_HEAP_WORKER_LAYOUT"
const memoryResultPrefix = "bufftree-heap-result: "

var memoryCases = []struct {
	layout, label string
}{
	{"Tree", "bufftree.BPTree"},
	{"GoMap", "builtin Go map"},
	{"Tidwall", "tidwall/btree.Map"},
	{"RBTree", "glycerine/rbtree"},
	{"Insdict", "insdict.Dict"},
}

type memoryResult struct {
	Layout  string
	Entries int
	Bytes   uint64
}

// TestMemoryUsage100K measures retained heap, not cumulative allocations.
// Each container is loaded in a fresh copy of this test process, without
// invoking testing.Benchmark. Run directly or with make memory.
func TestMemoryUsage100K(t *testing.T) {
	if layout := os.Getenv(memoryWorkerEnv); layout != "" {
		// The containers are loaded serially. One GC worker makes runtime
		// bookkeeping more stable without changing the containers' layouts.
		runtime.GOMAXPROCS(1)
		result := measureContainerHeap(t, layout)
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("%s%s\n", memoryResultPrefix, data)
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	type measurement struct {
		label string
		bytes uint64
	}
	var measurements []measurement
	for _, c := range memoryCases {
		cmd := exec.CommandContext(t.Context(), executable,
			"-test.run=^TestMemoryUsage100K$", "-test.count=1")
		cmd.Env = append(cmd.Environ(), memoryWorkerEnv+"="+c.layout)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", c.label, err, output)
		}
		var result memoryResult
		found := false
		for line := range bytes.SplitSeq(output, []byte("\n")) {
			if data, ok := bytes.CutPrefix(line, []byte(memoryResultPrefix)); ok {
				if err := json.Unmarshal(data, &result); err != nil {
					t.Fatalf("%s: %v", c.label, err)
				}
				found = true
				break
			}
		}
		if !found || result.Layout != c.layout || result.Entries != memoryEntries || result.Bytes == 0 {
			t.Fatalf("%s: invalid heap measurement %+v\n%s", c.label, result, output)
		}
		measurements = append(measurements, measurement{c.label, result.Bytes})
	}
	slices.SortStableFunc(measurements, func(a, b measurement) int {
		return cmp.Compare(b.bytes, a.bytes)
	})
	var rows [][]string
	for _, m := range measurements {
		rows = append(rows, []string{m.label, fmt.Sprint(m.bytes),
			fmt.Sprintf("%.2f", float64(m.bytes)/(1<<20)),
			fmt.Sprintf("%.2f", float64(m.bytes)/memoryEntries)})
	}

	fmt.Printf("\n%s %s/%s; 100,000 entries; uint64 keys and values\n",
		runtime.Version(), runtime.GOOS, runtime.GOARCH)
	fmt.Println("Retained heap: post-GC HeapAlloc minus pre-load baseline; each row uses a fresh process with GOMAXPROCS=1.")
	fmt.Println("Go map and insdict.Dict are pre-sized to 100,000 entries; insdict has not built its lazy sorted index.")
	fmt.Printf("\n%s\n", alignedBenchmarkTable([]string{"Container", "Heap bytes", "MiB", "B/key"}, rows))
	fmt.Println()
}

func measureContainerHeap(t *testing.T, name string) memoryResult {
	t.Helper()
	var makeContainer func() benchUint64Points
	for _, layout := range comparisonPointLayouts(memoryEntries) {
		if layout.name == name {
			makeContainer = layout.make
			break
		}
	}
	if makeContainer == nil {
		t.Fatalf("unknown memory layout %q", name)
	}

	// Warm the GC before establishing the baseline so its first-use state
	// does not get charged to the container. MemStats itself stays on the stack.
	runtime.GC()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	container := makeContainer()
	for i := 0; i < memoryEntries; i++ {
		container.Put(benchKey(i), uint64(i))
	}
	counted, ok := container.(interface{ Len() int })
	if !ok || counted.Len() != memoryEntries {
		t.Fatalf("%s: expected %d entries", name, memoryEntries)
	}
	for i := 0; i < memoryEntries; i++ {
		if value := container.Get(benchKey(i)); value != uint64(i) {
			t.Fatalf("%s: lookup %d returned %d", name, i, value)
		}
	}

	// Collect temporary growth/redistribution allocations while keeping the
	// complete container reachable through the final heap snapshot.
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(container)
	if after.HeapAlloc <= before.HeapAlloc {
		t.Fatalf("%s: heap did not grow: before=%d after=%d", name, before.HeapAlloc, after.HeapAlloc)
	}
	return memoryResult{Layout: name, Entries: memoryEntries, Bytes: after.HeapAlloc - before.HeapAlloc}
}
