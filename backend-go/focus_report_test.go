package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

// TestFocusReport is a tuning harness, not a regression test: it scores real
// photos so blurThreshold and the other constants at the top of focus.go can be
// checked against actual shoots rather than the synthetic scenes in
// focus_test.go. It is skipped unless FOCUS_REPORT_DIR names a folder:
//
//	FOCUS_REPORT_DIR=$HOME/focus-set go test -run TestFocusReport -v -timeout 30m .
//
// A folder holding sharp/ and blurry/ subfolders is a labelled set. Every
// photo in them is scored, and the report ends with the range of thresholds
// that would separate the two, what the current threshold gets wrong, and a
// sweep of how many keepers each threshold would mark for deletion and how
// many misses it would let through. Any other folder, such as an import
// session, is scored as it stands.
//
// Each row is tab separated: the score, the label (or "-"), the verdict at the
// current threshold, where the sharpest tile was as a percentage across and
// down the frame, and the file. Photos that cannot be read are listed last.
func TestFocusReport(t *testing.T) {
	root := os.Getenv("FOCUS_REPORT_DIR")
	if root == "" {
		t.Skip("set FOCUS_REPORT_DIR to a folder of photos to score them")
	}
	if rest, ok := strings.CutPrefix(root, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		root = filepath.Join(home, rest)
	}

	type photo struct {
		label, file string
		reading     focusReading
		err         error
	}
	var photos []*photo
	add := func(label, dir string) {
		names, err := listPhotoFiles(dir)
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		for _, name := range names {
			rel, _ := filepath.Rel(root, filepath.Join(dir, name))
			photos = append(photos, &photo{label: label, file: rel})
		}
	}
	labelled := isDir(filepath.Join(root, "sharp")) && isDir(filepath.Join(root, "blurry"))
	if labelled {
		add("sharp", filepath.Join(root, "sharp"))
		add("blurry", filepath.Join(root, "blurry"))
	} else {
		add("-", root)
	}
	if len(photos) == 0 {
		t.Fatalf("no photos in %s", root)
	}

	// Score in parallel, like the endpoint does.
	jobs := make(chan *photo)
	var wg sync.WaitGroup
	for i := 0; i < min(runtime.NumCPU(), maxFocusWorkers); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				p.reading, p.err = photoFocus(filepath.Join(root, p.file))
			}
		}()
	}
	for _, p := range photos {
		jobs <- p
	}
	close(jobs)
	wg.Wait()

	var scored, failed []*photo
	for _, p := range photos {
		if p.err != nil {
			failed = append(failed, p)
		} else {
			scored = append(scored, p)
		}
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].reading.score < scored[j].reading.score })

	verdict := func(score, threshold float64) string {
		if score < threshold {
			return "blurry"
		}
		return "sharp"
	}
	fmt.Printf("\nscore\tlabel\tverdict\tbest tile\tfile\n")
	for _, p := range scored {
		r := p.reading
		fmt.Printf("%.3f\t%s\t%s\t%.0f%%,%.0f%%\t%s\n", r.score, p.label, verdict(r.score, blurThreshold), 100*r.x, 100*r.y, p.file)
	}
	for _, p := range failed {
		fmt.Printf("-\t%s\tunreadable\t-\t%s (%v)\n", p.label, p.file, p.err)
	}
	fmt.Printf("\n%d photos scored at blurThreshold %.2f, %d unreadable\n", len(scored), blurThreshold, len(failed))
	if !labelled {
		return
	}

	// What a threshold would get wrong: keepers it marks for deletion, and
	// misses it lets through. A photo is marked when its score is below it.
	var sharp, blurry []*photo
	for _, p := range scored {
		if p.label == "sharp" {
			sharp = append(sharp, p)
		} else {
			blurry = append(blurry, p)
		}
	}
	mistakes := func(threshold float64) (marked, missed int) {
		for _, p := range sharp {
			if p.reading.score < threshold {
				marked++
			}
		}
		for _, p := range blurry {
			if p.reading.score >= threshold {
				missed++
			}
		}
		return marked, missed
	}
	if len(sharp) > 0 {
		fmt.Printf("sharp:  %d photos, lowest %.3f (%s)\n", len(sharp), sharp[0].reading.score, sharp[0].file)
	}
	if len(blurry) > 0 {
		top := blurry[len(blurry)-1]
		fmt.Printf("blurry: %d photos, highest %.3f (%s)\n", len(blurry), top.reading.score, top.file)
	}
	if len(sharp) > 0 && len(blurry) > 0 {
		lo, hi := blurry[len(blurry)-1].reading.score, sharp[0].reading.score
		if lo < hi {
			fmt.Printf("separable: any threshold above %.3f and no higher than %.3f marks every blurry photo and no sharp one\n", lo, hi)
		} else {
			fmt.Printf("not separable: the blurriest-scoring sharp photo is at or below the sharpest-scoring blurry one, so a threshold alone has to trade one kind of mistake for the other\n")
		}
	}
	marked, missed := mistakes(blurThreshold)
	fmt.Printf("at the current %.2f: %d of %d sharp photos would be marked for deletion, %d of %d blurry photos would be missed\n",
		blurThreshold, marked, len(sharp), missed, len(blurry))
	fmt.Printf("\nthreshold\tsharp marked\tblurry missed\n")
	for i := 4; i <= 30; i++ {
		threshold := float64(i) / 20
		marked, missed := mistakes(threshold)
		fmt.Printf("%.2f\t%d\t%d\n", threshold, marked, missed)
	}
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
