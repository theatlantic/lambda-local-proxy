package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/lambda"
	flags "github.com/jessevdk/go-flags"
)

type PayloadBuilder interface {
	BuildRequest(*http.Request) ([]byte, error)

	BuildResponse([]byte) (int, []byte, map[string][]string, error)
}

type statusResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func newStatusResponseWriter(w http.ResponseWriter) *statusResponseWriter {
	return &statusResponseWriter{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
	}
}

func (sw *statusResponseWriter) WriteHeader(statusCode int) {
	sw.statusCode = statusCode
	sw.ResponseWriter.WriteHeader(statusCode)
}

type Options struct {
	Function      string `env:"FUNCTION" short:"f" long:"function" description:"Lambda function name" default:"function"`
	Bind          string `env:"BIND" short:"l" long:"listen" description:"HTTP listen address"`
	Port          int    `env:"PORT" short:"p" long:"port" description:"HTTP listen port" default:"8080"`
	Endpoint      string `env:"ENDPOINT" short:"e" long:"endpoint" description:"Lambda API endpoint"`
	ApiType       string `env:"API_TYPE" short:"t" long:"type" description:"HTTP gateway type (\"alb\" for ALB)" default:"alb"`
	AlbMultiValue bool   `env:"ALB_MULTI_VALUE" short:"m" long:"multi-value" description:"Enable multi-value headers. Effective only with -t alb"`
}

func main() {
	log.Printf("Starting Lambda Proxy")

	if err := run(); err != nil {
		log.Fatalf("Error: %v", err)
	}

	log.Printf("Exiting Lambda Proxy")
}

func run() error {
	var opts Options
	_, err := flags.Parse(&opts)
	if err != nil {
		return fmt.Errorf("Failed to parse options: %v", err)
	}

	if opts.ApiType != "alb" {
		return fmt.Errorf("Unknown gateway type: " + opts.ApiType)
	}

	requestFree := make(chan bool, 1)
	requestFree <- true

	pb := NewALBPayloadBuilder(opts.AlbMultiValue)
	client := MakeLambdaClient(opts.Endpoint)
	handler := MakeInvokeLambdaHandler(client, opts.Function, pb, requestFree)

	http.HandleFunc("/", logger(handler))

	listenAddress := fmt.Sprintf("%s:%d", opts.Bind, opts.Port)
	log.Printf("Listening on %s", listenAddress)
	return http.ListenAndServe(listenAddress, nil)
}

func logger(handler func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := newStatusResponseWriter(w)

		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("Panic: %v", rec)
				WriteErrorResponse(w, "Panic", nil)
			} else {
				log.Printf("[%s] %d %s %s", r.Method, sw.statusCode, r.URL, time.Since(start))
			}
		}()

		handler(sw, r)
	}
}

func MakeLambdaClient(endpoint string) *lambda.Lambda {
	sess := session.Must(session.NewSessionWithOptions(session.Options{
		SharedConfigState: session.SharedConfigEnable,
	}))

	config := aws.Config{}
	if endpoint != "" {
		config.Endpoint = &endpoint
	}

	return lambda.New(sess, &config)
}

func MakeInvokeLambdaHandler(client *lambda.Lambda, functionName string, pb PayloadBuilder, requestFree chan bool) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		// Use the requestFree channel as a lock to prevent more than one inflight request to the lambda function
		// since it has a concurrency of one.
		_, ok := <-requestFree
		if !ok {
			return // Indicates channel closure
		}

		defer func() { requestFree <- true }()

		// Add proxy headers
		r.Header.Add("X-Forwarded-For", r.RemoteAddr[0:strings.LastIndex(r.RemoteAddr, ":")])
		r.Header.Add("X-Forwarded-Proto", "http")
		r.Header.Add("X-Forwarded-Port", "8080")

		// Parse HTTP response and create an event
		payload, err := pb.BuildRequest(r)
		if err != nil {
			WriteErrorResponse(w, "Invalid request", err)
			return
		}

		// Invoke Lambda with the event
		output, err := client.Invoke(&lambda.InvokeInput{
			FunctionName: aws.String(functionName),
			Payload:      payload,
		})
		if err != nil {
			WriteErrorResponse(w, "Failed to invoke Lambda", err)
			return
		}
		if output.FunctionError != nil {
			WriteErrorResponse(w, "Lambda function error: "+*output.FunctionError, nil)
			return
		}

		// Build a response
		status, body, headers, err := pb.BuildResponse(output.Payload)
		if err != nil {
			WriteErrorResponse(w, "Invalid JSON response", err)
			return
		}

		// Write the response - headers, status code, and body
		for key, values := range headers {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(status)
		w.Write(body)
		return
	}
}

func WriteErrorResponse(w http.ResponseWriter, message string, err error) {
	body := "502 Bad Gateway\n" + message
	if err != nil {
		body += "\n" + err.Error()
	}
	w.WriteHeader(502) // Bad Gateway
	w.Write([]byte(body))
	return
}
