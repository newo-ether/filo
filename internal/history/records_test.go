package history

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestRecordsCrossScanBoundariesAndIgnoreUnfinishedAppends(t *testing.T) {
	complete := []string{"header", strings.Repeat("a", scanBytes-8), "third\r", strings.Repeat("b", scanBytes*3)}
	text := strings.Join(complete, "\n\n") + "\nunfinished"
	reader := strings.NewReader(text)
	var got []string
	var oldest int64
	for span, err := range records(context.Background(), reader, int64(len(text))) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, text[span.start:span.end])
		oldest = span.start
	}
	want := []string{complete[3], complete[2], complete[1], complete[0]}
	if !reflect.DeepEqual(got, want) || oldest != 0 {
		t.Fatal("Reverse scan lost or split complete records")
	}
	for _, unfinished := range []string{"", "unfinished", strings.Repeat("x", scanBytes+1)} {
		for _, err := range records(context.Background(), strings.NewReader(unfinished), int64(len(unfinished))) {
			t.Fatalf("Unfinished record was published: %v", err)
		}
	}
}

func TestRecordsDetectTruncationCancellationAndEarlyConsumerExit(t *testing.T) {
	reader := strings.NewReader("first\nsecond\n")
	for _, err := range records(context.Background(), reader, reader.Size()+1) {
		if err == nil {
			t.Fatal("Short read accepted")
		}
		break
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, err := range records(ctx, reader, reader.Size()) {
		if err != context.Canceled {
			t.Fatal(err)
		}
	}
	count := 0
	for _, err := range records(context.Background(), reader, reader.Size()) {
		if err != nil {
			t.Fatal(err)
		}
		count++
		break
	}
	if count != 1 {
		t.Fatal("Consumer exit failed")
	}
}
