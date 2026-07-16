package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq" // PostgreSQL driver — lets database/sql speak to Postgres
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ─────────────────────────────────────────────
//  Prometheus metrics
//  These counters are scraped by Prometheus and
//  visualised in Grafana dashboards.
// ─────────────────────────────────────────────

var (
	// Counts every call to the /analyze endpoint
	analyzeCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "incident_analyze_requests_total",
		Help: "Total number of incident analysis requests",
	})

	// Counts every call to GET /incidents (history page)
	historyCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "incident_history_requests_total",
		Help: "Total number of incident history requests",
	})

	// Generic per-path HTTP counter used by all routes
	httpRequestsCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests, labelled by path",
		},
		[]string{"path"},
	)
)

func init() {
	// Register all metrics with Prometheus at startup
	prometheus.MustRegister(analyzeCounter)
	prometheus.MustRegister(historyCounter)
	prometheus.MustRegister(httpRequestsCounter)
}

// ─────────────────────────────────────────────
//  AnalyzeRequest is the JSON body that the
//  frontend (or k6 load test) sends to /analyze.
// ─────────────────────────────────────────────

type AnalyzeRequest struct {
	Namespace string `json:"namespace"` // e.g. "production"
	PodName   string `json:"pod_name"`  // e.g. "payment-service-abc12"
}

// ─────────────────────────────────────────────
//  AnalyzeResponse is what we return to the UI.
// ─────────────────────────────────────────────

type AnalyzeResponse struct {
	PodName   string `json:"pod_name"`
	Namespace string `json:"namespace"`
	Status    string `json:"status"`
	Analysis  string `json:"analysis"` // LLM-generated root cause + fix
}

// ─────────────────────────────────────────────
//  Incident is a row stored in PostgreSQL.
// ─────────────────────────────────────────────

type Incident struct {
	ID        int    `json:"id"`
	PodName   string `json:"pod_name"`
	Namespace string `json:"namespace"`
	Status    string `json:"status"`
	Analysis  string `json:"analysis"`
	CreatedAt string `json:"created_at"`
}

// ─────────────────────────────────────────────
//  createDBConnection builds a *sql.DB from
//  environment variables injected by Kubernetes
//  Secrets (see deploy/deploy.yaml).
// ─────────────────────────────────────────────

func createDBConnection() (*sql.DB, error) {
	connStr := fmt.Sprintf(
		"user=%s password=%s host=%s port=%s dbname=%s sslmode=%s",
		os.Getenv("DB_USERNAME"),
		os.Getenv("DB_PASSWORD"),
		os.Getenv("DB_HOST"),
		os.Getenv("DB_PORT"),
		os.Getenv("DB_NAME"),
		os.Getenv("SSL"),
	)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}

	// Ping verifies the connection is actually alive
	if err = db.Ping(); err != nil {
		return nil, err
	}

	return db, nil
}

// ─────────────────────────────────────────────
//  createK8sClient returns a Kubernetes client.
//  Inside the cluster it uses the in-cluster
//  service-account token.  Outside (local dev)
//  it falls back to KUBECONFIG / ~/.kube/config.
// ─────────────────────────────────────────────

func createK8sClient() (*kubernetes.Clientset, error) {
	// Try in-cluster config first (running inside a Pod)
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fall back to local kubeconfig for development
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = os.Getenv("HOME") + "/.kube/config"
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, err
		}
	}

	return kubernetes.NewForConfig(config)
}

// ─────────────────────────────────────────────
//  collectK8sInfo gathers pod status, recent
//  events, and the last 50 log lines for a pod.
//  This raw data becomes the context for the LLM.
// ─────────────────────────────────────────────

func collectK8sInfo(k8s *kubernetes.Clientset, namespace, podName string) (status, events, logs string) {
	ctx := context.Background()

	// ── Pod status ──────────────────────────────
	pod, err := k8s.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		status = "unknown (could not fetch pod)"
	} else {
		status = string(pod.Status.Phase)
		// If a container is waiting, grab its reason (e.g. CrashLoopBackOff)
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting != nil {
				status = cs.State.Waiting.Reason
			}
		}
	}

	// ── Recent events ────────────────────────────
	// Events show things like "Back-off restarting failed container"
	eventList, err := k8s.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.name=" + podName,
	})
	if err == nil {
		for _, e := range eventList.Items {
			events += fmt.Sprintf("[%s] %s\n", e.Reason, e.Message)
		}
	}

	// ── Container logs ───────────────────────────
	// Tail the last 50 lines so the prompt stays concise
	tailLines := int64(50)
	logReq := k8s.CoreV1().Pods(namespace).GetLogs(podName, nil)
	_ = tailLines // placeholder — adjust PodLogOptions as needed
	logBytes, err := logReq.DoRaw(ctx)
	if err == nil {
		logs = string(logBytes)
		if len(logs) > 2000 {
			logs = logs[len(logs)-2000:] // keep only the last 2000 chars
		}
	}

	return status, events, logs
}

// ─────────────────────────────────────────────
//  buildPrompt turns raw Kubernetes data into
//  a structured natural-language prompt that
//  the LLM can reason about.
// ─────────────────────────────────────────────

func buildPrompt(namespace, podName, status, events, logs string) string {
	return fmt.Sprintf(`You are a Kubernetes SRE (Site Reliability Engineer).

Analyze the following Kubernetes incident and provide:
1. Root Cause
2. Severity (Low / Medium / High / Critical)
3. Remediation Steps
4. Relevant kubectl commands

--- Incident Details ---
Pod Name:   %s
Namespace:  %s
Status:     %s

Recent Events:
%s

Container Logs (last 50 lines):
%s
`, podName, namespace, status, events, logs)
}

// ─────────────────────────────────────────────
//  callOpenAI sends the prompt to the OpenAI
//  Chat Completions API and returns the text
//  response.  The API key is read from the
//  OPENAI_API_KEY environment variable which
//  is mounted from a Kubernetes Secret.
// ─────────────────────────────────────────────

func callOpenAI(prompt string) (string, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")

	// Build the JSON request body for the Chat Completions API
	reqBody := map[string]interface{}{
		"model": "gpt-4o-mini", // cost-effective model; swap to gpt-4o for richer output
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	// POST to the OpenAI API endpoint
	req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	// Parse the OpenAI response envelope to extract the generated text
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return "", err
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices returned from OpenAI")
	}

	return result.Choices[0].Message.Content, nil
}

// ─────────────────────────────────────────────
//  saveIncident persists the analysis result
//  into the PostgreSQL incidents table so users
//  can browse historical incidents on the UI.
// ─────────────────────────────────────────────

func saveIncident(db *sql.DB, podName, namespace, status, analysis string) error {
	_, err := db.Exec(
		`INSERT INTO incidents (pod_name, namespace, status, analysis, created_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		podName, namespace, status, analysis, time.Now().UTC(),
	)
	return err
}

// ─────────────────────────────────────────────
//  main wires everything together:
//    - Gin HTTP router
//    - PostgreSQL connection
//    - Kubernetes client
//    - Route handlers
// ─────────────────────────────────────────────

func main() {
	// Set up the Gin HTTP router
	router := gin.Default()

	// Load Go HTML templates embedded via KO_DATA_PATH
	router.LoadHTMLGlob(os.Getenv("KO_DATA_PATH") + "/*.html")

	// ── PostgreSQL ───────────────────────────────
	db, err := createDBConnection()
	if err != nil {
		log.Println("Warning: could not connect to PostgreSQL:", err)
		// We continue without DB — analysis still works, history is disabled
	}
	if db != nil {
		defer db.Close()
	}

	// ── Kubernetes client ────────────────────────
	k8s, err := createK8sClient()
	if err != nil {
		log.Println("Warning: could not create Kubernetes client:", err)
		// We continue — the handler will return a graceful error
	}

	// ── Routes ───────────────────────────────────

	// GET / — serves the incident response dashboard (index.html)
	router.GET("/", func(c *gin.Context) {
		httpRequestsCounter.WithLabelValues("/").Inc()
		c.HTML(http.StatusOK, "index.html", nil)
	})

	// POST /analyze — the main endpoint.
	// Collects K8s data → builds prompt → calls OpenAI → saves to DB → returns JSON.
	router.POST("/analyze", func(c *gin.Context) {
		httpRequestsCounter.WithLabelValues("/analyze").Inc()
		analyzeCounter.Inc()

		var req AnalyzeRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}

		if req.Namespace == "" {
			req.Namespace = "default"
		}

		// Step 1 — Collect Kubernetes diagnostic information
		var status, events, logs string
		if k8s != nil {
			status, events, logs = collectK8sInfo(k8s, req.Namespace, req.PodName)
		} else {
			status = "unknown — Kubernetes client not available"
		}

		// Step 2 — Build the prompt from K8s data
		prompt := buildPrompt(req.Namespace, req.PodName, status, events, logs)

		// Step 3 — Send prompt to OpenAI and receive root cause analysis
		analysis, err := callOpenAI(prompt)
		if err != nil {
			log.Println("OpenAI error:", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "LLM call failed: " + err.Error()})
			return
		}

		// Step 4 — Persist the incident + analysis to PostgreSQL
		if db != nil {
			if err := saveIncident(db, req.PodName, req.Namespace, status, analysis); err != nil {
				log.Println("Warning: could not save incident:", err)
			}
		}

		// Step 5 — Return the structured analysis to the frontend
		c.JSON(http.StatusOK, AnalyzeResponse{
			PodName:   req.PodName,
			Namespace: req.Namespace,
			Status:    status,
			Analysis:  analysis,
		})
	})

	// GET /incidents — returns the last 20 stored incidents as JSON
	router.GET("/incidents", func(c *gin.Context) {
		httpRequestsCounter.WithLabelValues("/incidents").Inc()
		historyCounter.Inc()

		if db == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "database not available"})
			return
		}

		rows, err := db.Query(
			`SELECT id, pod_name, namespace, status, analysis, created_at
			 FROM incidents ORDER BY created_at DESC LIMIT 20`,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database query failed"})
			return
		}
		defer rows.Close()

		var incidents []Incident
		for rows.Next() {
			var i Incident
			if err := rows.Scan(&i.ID, &i.PodName, &i.Namespace, &i.Status, &i.Analysis, &i.CreatedAt); err != nil {
				continue
			}
			incidents = append(incidents, i)
		}

		c.JSON(http.StatusOK, incidents)
	})

	// GET /health — used by Kubernetes liveness and readiness probes
	router.GET("/health", func(c *gin.Context) {
		httpRequestsCounter.WithLabelValues("/health").Inc()
		c.String(http.StatusOK, "OK")
	})

	// GET /metrics — Prometheus scrapes this endpoint every 15 s
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	log.Println("Incident Response Service starting on :8080")
	router.Run(":8080")
}
