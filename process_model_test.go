package decalgo

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
)

func TestProcessModelProtocol(t *testing.T) {
	t.Setenv("DECALGO_MODEL_HELPER", "1")
	ctx := context.Background()
	model, err := NewProcessModel(ctx, os.Args[0], "-test.run=TestProcessModelProtocol")
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	if got := model.Fingerprint(); got != "helper-v1" {
		t.Fatalf("fingerprint %q", got)
	}
	ids, err := model.Tokenize(ctx, "abc")
	if err != nil || !equalInts(ids, []int{97, 98, 99}) {
		t.Fatalf("tokenize: %v, %v", ids, err)
	}
	text, err := model.Detokenize(ctx, ids)
	if err != nil || text != "abc" {
		t.Fatalf("detokenize: %q, %v", text, err)
	}
	candidates, err := model.Next(ctx, ids, 4)
	if err != nil || len(candidates) != 4 {
		t.Fatalf("next: %v, %v", candidates, err)
	}
}

func runModelHelper() {
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request modelRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return
		}
		response := modelResponse{OK: true}
		switch request.Op {
		case "info":
			response.Fingerprint = "helper-v1"
		case "tokenize":
			for _, r := range request.Text {
				response.Tokens = append(response.Tokens, int(r))
			}
		case "detokenize":
			for _, id := range request.Tokens {
				response.Text += string(rune(id))
			}
		case "next":
			for i := 0; i < request.TopN; i++ {
				response.Candidates = append(response.Candidates, TokenCandidate{ID: i, LogProb: float64(-i)})
			}
		default:
			response.OK = false
			response.Error = fmt.Sprintf("unknown op %s (%s)", request.Op, strconv.Itoa(request.TopN))
		}
		if err := encoder.Encode(response); err != nil {
			return
		}
	}
}

func TestMain(m *testing.M) {
	if os.Getenv("DECALGO_MODEL_HELPER") == "1" {
		runModelHelper()
		os.Exit(0)
	}
	os.Exit(m.Run())
}
