package perf

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"
)

// csvHeader is the order of the columns in the CSV QAWork writes.
var csvHeader = []string{"at", "fps", "frame_ms", "jank_pct", "cpu_pct", "mem_mb", "temp_c", "threads", "note"}

// WriteCSV writes samples as RFC 4180 CSV with one header row.
func WriteCSV(w io.Writer, samples []Sample) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(csvHeader); err != nil {
		return err
	}
	for _, s := range samples {
		rec := []string{
			s.At.Format(time.RFC3339Nano),
			formatFloat(s.FPS), formatFloat(s.FrameMS), formatFloat(s.JankPct),
			formatFloat(s.CPU), formatFloat(s.MemMB), formatFloat(s.TempC),
			strconv.Itoa(s.Threads), s.Note,
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// SaveCSV writes samples to path.
func SaveCSV(path string, samples []Sample) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return WriteCSV(f, samples)
}

// ReadCSV reads samples written by WriteCSV.
func ReadCSV(r io.Reader) ([]Sample, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("CSV 为空")
	}
	var samples []Sample
	for i, row := range rows {
		if i == 0 {
			continue // header
		}
		if len(row) < 8 {
			return nil, fmt.Errorf("第 %d 行字段不足: %v", i+1, row)
		}
		at, err := time.Parse(time.RFC3339Nano, row[0])
		if err != nil {
			return nil, fmt.Errorf("第 %d 行时间无效: %w", i+1, err)
		}
		s := Sample{At: at}
		s.FPS = parseFloat(row[1])
		s.FrameMS = parseFloat(row[2])
		s.JankPct = parseFloat(row[3])
		s.CPU = parseFloat(row[4])
		s.MemMB = parseFloat(row[5])
		s.TempC = parseFloat(row[6])
		s.Threads = int(parseFloat(row[7]))
		if len(row) > 8 {
			s.Note = row[8]
		}
		samples = append(samples, s)
	}
	return samples, nil
}

// LoadCSV reads samples from path.
func LoadCSV(path string) ([]Sample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadCSV(f)
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// RunCapture samples until ctx is done or duration elapses, returning the
// samples taken. onSample, when set, sees each sample as it arrives.
func RunCapture(ctx context.Context, s Sampler, interval time.Duration, onSample func(Sample)) ([]Sample, error) {
	if interval < 200*time.Millisecond {
		interval = 200 * time.Millisecond
	}
	if err := s.Start(ctx); err != nil {
		return nil, err
	}
	defer s.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var samples []Sample
	for {
		select {
		case <-ctx.Done():
			return samples, nil
		case <-ticker.C:
			sample, err := s.Sample(ctx)
			if err != nil {
				continue // a transient read failure skips one interval
			}
			samples = append(samples, sample)
			if onSample != nil {
				onSample(sample)
			}
		}
	}
}
