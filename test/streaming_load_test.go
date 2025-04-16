package test

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/camunda-community-hub/zeebe-client-go/v8/pkg/entities"
	"github.com/camunda-community-hub/zeebe-client-go/v8/pkg/worker"
	"github.com/camunda-community-hub/zeebe-client-go/v8/pkg/zbc"
	"go.opentelemetry.io/otel"
)

var (
	startedCounter    atomic.Int64
	completedCounter  atomic.Int64
	lastHandleJobTime time.Time = time.Now()
	zeebeUrl          string    = "zeebe.enp-dev....corp.ru:26500"
)

// TestLoad - shows the problem
func TestLoad(t *testing.T) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	// client for worker
	client, err := zbc.NewClient(&zbc.ClientConfig{
		GatewayAddress:         zeebeUrl,
		UsePlaintextConnection: true,
		UserAgent:              "gobase-zeebe-test",
	})
	if err != nil {
		log.Fatalf("Failed to create Zeebe client: %v", err)
		return
	}

	// client for start process
	client2, err := zbc.NewClient(&zbc.ClientConfig{
		GatewayAddress:         zeebeUrl,
		UsePlaintextConnection: true,
		UserAgent:              "gobase-zeebe-test",
	})
	if err != nil {
		log.Fatalf("Failed to create Zeebe client: %v", err)
		return
	}

	// issue happen when specific setting combination:
	// - MaxJobActivate
	// - Concurrency
	// - PollThreshold
	// - worker complete time, for this test means delay in handleJob method
	// to repeat the error different settings may be needed.
	// pattern: process should be started faster, then worker complete jobs

	jobWorker := client.NewJobWorker().
		JobType("LoadTestWorker").
		Handler(handleJob).
		Name("example-worker").
		MaxJobsActive(4).
		Concurrency(2).
		Timeout(time.Minute * 5).
		PollInterval(10 * time.Second).
		PollThreshold(0.3).
		RequestTimeout(10 * time.Second).
		StreamEnabled(true).
		Open()
	defer jobWorker.Close()

	ctx := context.Background()

	tracer := otel.Tracer("example-tracer")

	ctx, parentSpan := tracer.Start(ctx, "parent-span")
	defer parentSpan.End()

	traceID := parentSpan.SpanContext().TraceID().String()

	log.Printf("Root traceId: %s", traceID)

	wg := &sync.WaitGroup{}

	for i := 0; i < 500; i++ {
		go runProcess(ctx, client2, wg)
	}

	log.Print("Completed")

	for {
		select {
		case <-ticker.C: // Каждый раз, когда Ticker срабатывает
			log.Printf("Started/completed: %d/%d", startedCounter.Load(), completedCounter.Load())

		}
		if time.Since(lastHandleJobTime) > time.Second*30 {
			break
		}
	}
}

// TestCompleteStartedJobs - fast complete freeze jobs
func TestCompleteStartedJobs(t *testing.T) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Создаем клиента Zeebe
	client, err := zbc.NewClient(&zbc.ClientConfig{
		GatewayAddress:         zeebeUrl,
		UsePlaintextConnection: true,
		UserAgent:              "gobase-zeebe-test",
	})
	if err != nil {
		log.Fatalf("Failed to create Zeebe client: %v", err)
		return
	}

	jobWorker := client.NewJobWorker().
		JobType("LoadTestWorker").
		Handler(handleJob).
		Name("example-worker").
		MaxJobsActive(50).
		Concurrency(50).
		Timeout(time.Minute * 5).
		PollInterval(10 * time.Second).
		PollThreshold(0.3).
		RequestTimeout(10 * time.Second).
		StreamEnabled(false).
		Open()
	defer jobWorker.Close()

	for {
		select {
		case <-ticker.C: // Каждый раз, когда Ticker срабатывает
			log.Printf("completed: %d", completedCounter.Load())
		}
		if time.Since(lastHandleJobTime) > time.Second*10 {
			break
		}
	}

}

var counter = 0

func runProcess(ctx context.Context, client zbc.Client, wg *sync.WaitGroup) {
	wg.Add(1)
	defer wg.Done()

	tracer := otel.Tracer("example-tracer")

	_, childSpan := tracer.Start(ctx, "child-span")
	defer childSpan.End()

	traceParent := fmt.Sprintf("00-%s-%s-01", childSpan.SpanContext().TraceID(), childSpan.SpanContext().SpanID())

	processID := "LostJobTest"
	variables := map[string]interface{}{
		"counter": counter,
		"httpVariables": map[string]interface{}{
			"urlX": "http://aaa.com:9801/GetInfo",
		},
		"logContext": map[string]interface{}{
			"customerIdToken": "",
			"Features":        childSpan.SpanContext().SpanID(),
			"correlationId":   "2737e8cc-17e4-4a09-a338-257a104d37aa",
			"traceparent":     traceParent,
		},
	}

	request, err := client.NewCreateInstanceCommand().BPMNProcessId(processID).LatestVersion().VariablesFromMap(variables)
	if err != nil {
		log.Fatalf("Failed to create instance command: %v", err)
	}

	response, err := request.Send(ctx)
	if err != nil {
		log.Fatalf("Failed to send create instance command: %v", err)
	}

	startedCounter.Add(1)
	log.Printf("processKey: %d, startedCounter: %d", response.ProcessInstanceKey, startedCounter.Load())
}

func handleJob(client worker.JobClient, job entities.Job) {
	jobKey := job.GetKey()

	time.Sleep(200 * time.Millisecond)

	completedCounter.Add(1)

	lastHandleJobTime = time.Now()

	_, err := client.NewCompleteJobCommand().JobKey(jobKey).Send(context.Background())
	if err != nil {
		log.Printf("Error processing job with key: %d\n", jobKey)
	}

	log.Printf("Completed job with key: %d, completedCounter: %d\n", job.ProcessInstanceKey, completedCounter.Load())
}
