package bench

import (
	"strings"
	"testing"
	"time"
)

func TestReadmeBenchmarkTables(t *testing.T) {
	values := make(map[string]float64)
	for _, name := range readmeBenchmarkNames() {
		values[name] = 123.456
	}
	values["Points/Tree/GetHit"] = 22.57
	values["Points/Tree/Update"] = 108.6
	values["Points/Tree/FreshPut"] = 456.7
	values["Points/Dict/FreshPut"] = 567.8
	values["Points/GoMap/FreshPut"] = 89.1
	values["Points/Tidwall/FreshPut"] = 201.2
	values["Points/RBTree/FreshPut"] = 300.3
	values["Iteration/Tree/Scan100000"] = 4.713
	values["Iteration/Dict/Iterate"] = 2.844
	simple, detailed, err := formatReadmeBenchmarkTables(values)
	if err != nil {
		t.Fatal(err)
	}
	if len(readmeBenchmarkNames()) != 29 {
		t.Fatal("shared table cells should reuse the same measurements")
	}
	for _, table := range []string{simple, detailed} {
		lines := strings.Split(strings.TrimSpace(table), "\n")
		var pipes []int
		for i, ch := range lines[0] {
			if ch == '|' {
				pipes = append(pipes, i)
			}
		}
		for _, line := range lines {
			if len(line) != len(lines[0]) || strings.ContainsRune(line, '\t') {
				t.Fatal("table must use spaces to align all rows")
			}
			for _, column := range pipes {
				if line[column] != '|' {
					t.Fatalf("misaligned column %d in %q", column, line)
				}
			}
		}
	}
	short := strings.Split(strings.TrimSpace(simple), "\n")
	full := strings.Split(strings.TrimSpace(detailed), "\n")
	for i, selected := range []int{0, 2, 8, 9} {
		shortCells := strings.Split(short[i+2], "|")
		fullCells := strings.Split(full[selected+2], "|")
		for col := 2; col <= 5; col++ {
			if strings.TrimSpace(shortCells[col]) != strings.TrimSpace(fullCells[col]) {
				t.Fatal("simplified table must use the detailed table's BP-tree measurements")
			}
		}
	}
	for i, want := range []string{"22.6", "108.6", "4.71", "2.84"} {
		if got := strings.TrimSpace(strings.Split(short[i+2], "|")[2]); got != want {
			t.Fatalf("simplified value %q, want %q", got, want)
		}
	}
	if !strings.Contains(detailed, "not supported") {
		t.Fatal("Go map ordered scans must be marked unsupported")
	}
	for _, fresh := range []struct {
		row   int
		label string
		want  []string
	}{
		{3, "Tree `Put`, fresh key", []string{"456.7", "89.1", "201.2", "300.3"}},
		{6, "Dict `Put`, fresh key", []string{"567.8", "89.1", "201.2", "300.3"}},
	} {
		cells := strings.Split(full[fresh.row+2], "|")
		if strings.TrimSpace(cells[1]) != fresh.label {
			t.Fatalf("missing fresh Put row %q", fresh.label)
		}
		for i, want := range fresh.want {
			if got := strings.TrimSpace(cells[i+2]); got != want {
				t.Fatalf("%s column %d: got %q, want %q", fresh.label, i, got, want)
			}
		}
	}
	delete(values, "Points/Tree/GetHit")
	if _, _, err := formatReadmeBenchmarkTables(values); err == nil {
		t.Fatal("missing timing must not silently become zero")
	}
}

func TestReadmeBenchmarkTiming(t *testing.T) {
	result := testing.BenchmarkResult{N: 2, T: 3 * time.Nanosecond, Extra: map[string]float64{"iter_ns/key": 0.25}}
	if value, err := readmeBenchmarkTime(result, false); err != nil || value != 1.5 {
		t.Fatal("point time must retain fractional nanoseconds", value, err)
	}
	if value, err := readmeBenchmarkTime(result, true); err != nil || value != 0.25 {
		t.Fatal("traversal time must use the per-key metric", value, err)
	}
	delete(result.Extra, "iter_ns/key")
	if _, err := readmeBenchmarkTime(result, true); err == nil {
		t.Fatal("traversal must not fall back to aggregate time")
	}
	if value := readmeBenchmarkMedian([]float64{100, 1, 2}); value != 2 {
		t.Fatal("odd sample median", value)
	}
	if value := readmeBenchmarkMedian([]float64{100, 1, 2, 3}); value != 2.5 {
		t.Fatal("even sample median", value)
	}
}
