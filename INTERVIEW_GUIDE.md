# Interview Guide — AI-Powered Kubernetes Incident Response Platform

> **How to use this file:**
> Read each section out loud before your interview.
> Every explanation is written exactly how you would say it — simple, confident, technical.

---

## One-Paragraph Summary
*(Say this when the interviewer asks "tell me about your project")*

> "I built a cloud-native platform that automatically investigates Kubernetes incidents using AI.
> When a pod crashes or a deployment fails, instead of an engineer manually collecting logs and events,
> my application does that automatically — it pulls the pod status, Kubernetes events, and container logs,
> sends all of that as a prompt to the OpenAI API, and gets back a probable root cause and suggested fix.
> The backend is written in Go, deployed on Kubernetes using ArgoCD for GitOps, stores incident history
> in a PostgreSQL database managed by CloudNativePG, and exposes metrics to Prometheus and Grafana."

---

## The Full Flow — How Everything Connects
*(Say this when asked "walk me through the system")*

```
1.  Developer pushes code to GitHub
           ↓
2.  KO builds the Go binary into a container image (no Dockerfile needed)
    and pushes it to Docker Hub
           ↓
3.  deploy/deploy.yaml is updated with the new image tag and committed to Git
           ↓
4.  ArgoCD detects the Git change and applies the new YAML to Kubernetes
           ↓
5.  Kubernetes runs 3 replicas of the Go app (scales to 10 with HPA under load)
           ↓
6.  A pod has an incident — e.g. CrashLoopBackOff
           ↓
7.  Engineer opens the dashboard at app.incident-response.com
    (HTTPS → Gateway → Service → Go pod)
           ↓
8.  Engineer types the pod name and namespace, clicks Analyze
           ↓
9.  Go backend:
    Step 1 — Calls Kubernetes API → fetches pod status, events, last 50 log lines
    Step 2 — Formats that data into a structured prompt
    Step 3 — Sends prompt to OpenAI API (gpt-4o-mini)
    Step 4 — Receives root cause + remediation steps
    Step 5 — Saves incident to PostgreSQL (CloudNativePG)
    Step 6 — Returns JSON to the dashboard
           ↓
10. Dashboard shows: Root Cause, Severity, Remediation Steps, kubectl commands
           ↓
11. Prometheus scrapes /metrics every 15 s
    Grafana shows: request rates, latency, CPU, memory, pod count
```

---

## Every File — Plain English

---

### `main.go` — The Brain of the Application

**What it is:** The entire Go backend in one file. Five clear responsibilities.

**What to say:**

> "This is my Go backend. It uses the Gin framework to expose REST API endpoints.
> When someone calls POST /analyze with a pod name and namespace, the application does five things:
> First, it connects to the Kubernetes API using the client-go library and collects the pod's status,
> recent events, and the last 50 lines of logs.
> Second, it formats all of that into a structured prompt — like writing a detailed ticket for an SRE.
> Third, it sends that prompt to the OpenAI API using a plain HTTP POST request.
> Fourth, it saves the incident and the AI's response into PostgreSQL.
> Fifth, it returns the root cause analysis as JSON to whoever called the API."

**The five functions:**

| Function | What it does |
|---|---|
| `createDBConnection()` | Reads DB credentials from environment variables (injected by K8s Secrets) and opens a PostgreSQL connection |
| `createK8sClient()` | Creates a Kubernetes client — uses the in-cluster service account token when running inside a Pod, or `~/.kube/config` on your laptop |
| `collectK8sInfo()` | Asks Kubernetes: what is this pod's phase? what events happened? what do the logs say? Returns all three as strings |
| `buildPrompt()` | Takes the raw K8s data and formats it into a clear question for the LLM — includes pod name, namespace, status, events, and logs |
| `callOpenAI()` | POSTs the prompt to `api.openai.com/v1/chat/completions`, parses the JSON response, and returns the generated text |
| `saveIncident()` | Writes pod name, namespace, status, and the AI analysis into the PostgreSQL `incidents` table |

**The API endpoints:**

| Endpoint | What it does |
|---|---|
| `POST /analyze` | Main endpoint — runs the full K8s → Prompt → OpenAI → DB pipeline |
| `GET /incidents` | Returns the last 20 incidents stored in PostgreSQL as JSON |
| `GET /` | Serves the web dashboard HTML page |
| `GET /health` | Returns "OK" — used by Kubernetes liveness and readiness probes |
| `GET /metrics` | Returns Prometheus metrics — scraped every 15 seconds |

---

### `kodata/index.html` — The Web Dashboard

**What it is:** The frontend. A single HTML page served directly by the Go backend using Gin HTML templates.

**What to say:**

> "I used Go HTML templates rendered by Gin rather than a separate React frontend
> because my focus was on the backend and Kubernetes integration.
> The dashboard has three sections:
> a flow diagram showing how the system works end to end,
> a form where you enter a pod name and namespace and click Analyze,
> and a history table showing the last 20 incidents stored in the database.
> When you click Analyze, the page sends a POST /analyze request to the Go backend
> using the browser's fetch API and displays the LLM response with a colour-coded status badge —
> red for CrashLoopBackOff, green for Running, orange for Pending."

---

### `go.mod` — The Dependency List

**What it is:** Declares which external libraries the project uses. Like `package.json` in Node.js.

**What to say:**

> "This file tells Go which libraries to download when you build the project.
> The key ones are:
> gin for the HTTP server,
> lib/pq which is the PostgreSQL driver,
> prometheus/client_golang to expose metrics,
> and k8s.io/client-go and k8s.io/apimachinery which are the official Kubernetes client libraries
> that let my Go code talk to the Kubernetes API to fetch pods, logs, and events."

---

### `deploy/deploy.yaml` — How the App Runs on Kubernetes

**What it is:** Three Kubernetes resources in one file — Deployment, Service, Gateway, and HTTPRoute.

**What to say:**

> "This is the main Kubernetes manifest. It has four parts.
>
> The Deployment tells Kubernetes to run 3 replicas of my Go application.
> It also injects all configuration — database credentials and the OpenAI API key —
> as environment variables sourced from Kubernetes Secrets, so no passwords are hardcoded anywhere.
> It also has readiness and liveness probes hitting /health so Kubernetes knows
> whether a pod is healthy before sending traffic to it.
>
> The Service is a ClusterIP service — it load-balances traffic across all 3 pods inside the cluster.
>
> The Gateway and HTTPRoute use the Kubernetes Gateway API, which is the modern replacement for Ingress.
> It handles HTTPS with TLS termination so the pods only deal with plain HTTP internally."

---

### `hpa.yaml` — Auto-Scaling

**What it is:** A HorizontalPodAutoscaler — automatically adds or removes pods based on CPU and memory load.

**What to say:**

> "This tells Kubernetes to automatically scale the number of pods between 1 and 10.
> If CPU usage goes above 20% on average, or memory above 350MB per pod, Kubernetes adds more pods.
> This was directly observable during k6 load testing — when I sent 100 concurrent requests,
> I could watch in Grafana as Kubernetes added pods to handle the load,
> then scaled back down after the test finished."

---

### `pg_cluster.yaml` — The PostgreSQL Database

**What it is:** A CloudNativePG Cluster resource — creates a production-grade PostgreSQL cluster inside Kubernetes.

**What to say:**

> "Instead of running a plain PostgreSQL Docker container, I used CloudNativePG,
> which is a Kubernetes operator that manages PostgreSQL as a native Kubernetes workload.
> This YAML creates a 3-instance cluster — one primary and two replicas —
> with automatic failover, so if the primary crashes a replica is promoted automatically.
> The database stores the incident history — every AI analysis is saved here."

---

### `servicemonitor.yaml` — Connecting the App to Prometheus

**What it is:** A ServiceMonitor — tells the Prometheus operator where to scrape metrics from.

**What to say:**

> "Prometheus does not automatically know which applications to monitor.
> The ServiceMonitor tells the kube-prometheus-stack operator:
> go to the service labelled app: my-app, hit its /metrics endpoint every 15 seconds, and collect the metrics.
> The metrics my app exposes include how many incident analysis requests were made,
> how many history requests were made, and a counter per API path.
> These appear in Grafana dashboards."

---

### `prometheus.yml` — Local Prometheus Config

**What it is:** A minimal Prometheus config for local Docker-based development only.

**What to say:**

> "This is only used when running Prometheus locally with Docker during development.
> It tells Prometheus to scrape my Go app every 15 seconds.
> In the Kubernetes cluster, Prometheus is configured through the ServiceMonitor resource instead —
> this file was just for testing on my laptop."

---

### `cluster_issuer.yaml` — Automatic HTTPS Certificates

**What it is:** A cert-manager ClusterIssuer — automates TLS certificate generation from Let's Encrypt.

**What to say:**

> "This tells cert-manager to automatically get a free HTTPS certificate from Let's Encrypt
> for the domain app.incident-response.com.
> cert-manager handles the entire ACME challenge — it proves domain ownership,
> receives the certificate, stores it as a Kubernetes Secret,
> and automatically renews it before it expires.
> I never touched a certificate file manually."

---

### `argo-gateway.yaml` — The Traffic Entry Point

**What it is:** A Kubernetes Gateway — the single entry point for all external traffic into the cluster.

**What to say:**

> "Think of the Gateway as the front door of the cluster.
> All external traffic enters through this one Gateway.
> It listens on port 80 for HTTP and port 443 for HTTPS,
> and handles TLS termination at the edge so backend pods only see plain HTTP.
> The same Gateway handles two domains:
> app.incident-response.com for the incident response platform,
> and argo.incident-response.com for the ArgoCD dashboard."

---

### `route-argo.yaml` — ArgoCD Traffic Route

**What it is:** An HTTPRoute that tells the Gateway how to reach the ArgoCD UI.

**What to say:**

> "This HTTPRoute tells the Gateway: when a request comes in for argo.incident-response.com,
> forward it to the argocd-server service in the argocd namespace.
> Because the Gateway is in the default namespace and ArgoCD is in its own namespace,
> I also needed the ReferenceGrant to explicitly permit that cross-namespace routing."

---

### `referencegrant` — Cross-Namespace Permission

**What it is:** A ReferenceGrant — security permission that allows routing across Kubernetes namespaces.

**What to say:**

> "By default, a Gateway in one namespace cannot route to a Service in another namespace.
> This ReferenceGrant is an explicit security permission that allows the HTTPRoute in the default namespace
> to point to the argocd-server service in the argocd namespace.
> Without this file, the ArgoCD routing would silently fail."

---

### `load.js` — Load Testing Script

**What it is:** A k6 script that simulates 100 concurrent users hitting the /analyze endpoint.

**What to say:**

> "This is my k6 load test, run with k6 run load.js.
> It simulates 100 virtual users — think of them as 100 engineers simultaneously submitting
> incident analysis requests — for 30 seconds.
> Each user sends a POST /analyze request with a sample pod name and namespace,
> and the script checks that the response has a 200 status and contains an analysis field.
> While the test runs, I watch Grafana to see response time, throughput, error rate,
> and whether the HPA is spinning up more pods.
> The goal is not just to measure latency — it is to verify that the whole system
> stays stable and scales correctly under real concurrent load."

---

### `tmpl/deploy.j2` — Deployment Template for CI/CD

**What it is:** A Jinja2 template version of the deployment YAML used in automated pipelines.

**What to say:**

> "This is the same as deploy/deploy.yaml but with a placeholder for the container image tag.
> In a CI/CD pipeline, after KO builds and pushes a new image,
> a script fills in the actual image tag into this template and commits the updated YAML to Git.
> ArgoCD detects the commit and automatically deploys the new version.
> This is the GitOps pattern — Git is always the single source of truth
> for what version is running in the cluster."

---

## Common Interview Questions — Short Answers

**Q: Why Go?**
> "Go is lightweight, compiles to a single binary, and has excellent Kubernetes tooling. The official Kubernetes client library — client-go — is written in Go, and most tools like kubectl and Helm are also Go. It was the natural choice."

**Q: Why KO instead of Docker?**
> "KO is purpose-built for Go. It compiles the binary, wraps it in a Distroless base image, and pushes to the registry — no Dockerfile needed. Smaller image, less maintenance, and no Docker daemon dependency. Since my entire application is Go, there was no reason to use Docker."

**Q: What did you actually write in Go?**
> "The entire backend: the Gin router, the Kubernetes client that fetches pod status, events, and logs using client-go, the prompt builder that structures that data into a question for the LLM, the HTTP client that calls the OpenAI API and parses the response, the PostgreSQL integration that saves incident history, and the Prometheus metrics. All of that is in main.go."

**Q: What does client-go do?**
> "It is the official Go library for talking to the Kubernetes API — the same API that kubectl uses. I use it to get a pod's phase and container status, list events filtered to a specific pod, and stream the container logs. Without it, I would have to make raw HTTP calls to the Kubernetes API server manually."

**Q: Where is the OpenAI API key stored?**
> "It is stored as a Kubernetes Secret and injected into the pod as an environment variable at runtime. It is never in the source code or in the container image. The deploy.yaml references the Secret by name, and Kubernetes mounts the value at startup."

**Q: Why CloudNativePG instead of a plain Postgres container?**
> "A plain Postgres Docker container loses data if the node restarts and has no HA. CloudNativePG is a Kubernetes operator — it manages PostgreSQL natively, giving you automatic failover with a primary and two replicas, connection pooling, and backup support. It is how you run Postgres properly in Kubernetes."

**Q: What did you observe during k6 load testing?**
> "API response time increased as concurrent requests grew because each request makes an OpenAI API call which takes 2 to 5 seconds. I watched Kubernetes HPA scale from 3 pods up to more replicas under the load spike, and scale back down after the test. All of this was visible in real time on Grafana through the Prometheus metrics."

**Q: Why Gateway API instead of Ingress?**
> "Gateway API is the official successor to Ingress. It separates the Gateway infrastructure from the routing rules, supports cross-namespace routing with ReferenceGrants, and has a cleaner extensibility model. Ingress is being phased out in favour of it."

**Q: Why did the LLM only get Kubernetes data and not Prometheus metrics?**
> "Kubernetes state is sufficient to diagnose most incidents — CrashLoopBackOff, ImagePullBackOff, OOMKilled, missing ConfigMaps. For a first version this worked well. In a production-ready version I would also query Prometheus using PromQL to add CPU usage, memory trends, error rates, and request latency to the prompt. That combination of infrastructure state plus historical metrics is how production AI observability tools work."
