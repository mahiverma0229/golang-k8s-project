# AI-Powered Cloud-Native Incident Response Platform

Automates Kubernetes incident investigation using the OpenAI API.  
When a pod crashes or misbehaves, this platform collects Kubernetes diagnostic data, sends it to an LLM, and returns a probable root cause with remediation steps — in seconds instead of minutes.

---

## Architecture

```
Developer
    │
    ▼
GitHub Repository
    │  (GitOps — every change is a git commit)
    ▼
ArgoCD
    │  (syncs the cluster to match git state)
    ▼
Kubernetes Cluster
    │
    ▼
Go Incident Response Service  ← built with KO (no Dockerfile)
    │
 ┌──┼─────────────────┐
 │  │                 │
 ▼  ▼                 ▼
K8s API          OpenAI API        CloudNativePG
(Pods, Logs,     (gpt-4o-mini)     (Incident History)
 Events)              │
                      ▼
               Root Cause Analysis
               + Suggested Fix
                      │
                      ▼
              Grafana Dashboards
                      │
              k6 Load Testing
```

---

## How the AI Analysis Works

1. **Collect** — the Go backend calls the Kubernetes API (`client-go`) to fetch:
   - Pod phase / container status (e.g. `CrashLoopBackOff`)
   - Recent Kubernetes Events (e.g. _"Back-off restarting failed container"_)
   - Last 50 lines of container logs
2. **Build Prompt** — the data is formatted into a structured prompt
3. **Call OpenAI** — an HTTP POST is sent to the OpenAI Chat Completions API
4. **Parse Response** — the LLM returns root cause, severity, and kubectl remediation steps
5. **Store** — the incident + analysis is saved to PostgreSQL (CloudNativePG)
6. **Display** — the dashboard shows the analysis and incident history

```
Go Backend → Collect K8s Data → Build Prompt → OpenAI API → Parse Response → Dashboard
```

---

## Tech Stack

| Component | Technology |
|---|---|
| Backend language | Go |
| HTTP framework | Gin |
| Kubernetes client | `client-go` |
| LLM | OpenAI API (`gpt-4o-mini`) |
| Database | CloudNativePG (PostgreSQL) |
| Container image build | KO (no Dockerfile needed) |
| GitOps / CD | ArgoCD |
| Ingress / Traffic | Kubernetes Gateway API + NGINX Gateway Fabric |
| TLS certificates | cert-manager + Let's Encrypt |
| Monitoring | Prometheus + Grafana |
| Load testing | k6 |

---

## Why KO instead of Docker?

KO is designed specifically for Go applications.  
It compiles the Go binary, packages it into a Distroless OCI image, and pushes it to the registry — without a Dockerfile.

| | Docker | KO |
|---|---|---|
| Dockerfile required | Yes | No |
| Build method | Inside Docker daemon | Directly from Go source |
| Image size | Larger | Smaller (Distroless base) |
| Language support | Any | Go only |

---

## API Endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/` | Incident response dashboard (HTML) |
| `POST` | `/analyze` | Analyze a pod — returns LLM root cause + fix |
| `GET` | `/incidents` | Last 20 stored incidents (JSON) |
| `GET` | `/health` | Kubernetes liveness / readiness probe |
| `GET` | `/metrics` | Prometheus scrape endpoint |

**POST /analyze request body:**
```json
{
  "pod_name":  "payment-service-abc12",
  "namespace": "production"
}
```

**POST /analyze response:**
```json
{
  "pod_name":   "payment-service-abc12",
  "namespace":  "production",
  "status":     "CrashLoopBackOff",
  "analysis":   "Root Cause: Application cannot connect to PostgreSQL...\nRemediation: ..."
}
```

---

## Running Locally

### 1. Start PostgreSQL, Prometheus, Grafana

```bash
# PostgreSQL
docker run --name local-postgres \
  -e POSTGRES_USER=myuser \
  -e POSTGRES_PASSWORD=mypassword \
  -e POSTGRES_DB=incidents_db \
  -p 5432:5432 -d postgres

# Create the incidents table
docker exec -it local-postgres psql -U myuser -d incidents_db -c "
CREATE TABLE incidents (
    id         SERIAL PRIMARY KEY,
    pod_name   TEXT NOT NULL,
    namespace  TEXT NOT NULL,
    status     TEXT,
    analysis   TEXT,
    created_at TIMESTAMP
);
"

# Prometheus
docker run -d --name prometheus -p 9090:9090 \
  -v $(pwd)/prometheus.yml:/etc/prometheus/prometheus.yml \
  prom/prometheus

# Grafana
docker run -d --name grafana -p 3000:3000 grafana/grafana
```

### 2. Run the Go service

```bash
export DB_USERNAME=myuser
export DB_PASSWORD=mypassword
export DB_HOST=localhost
export DB_PORT=5432
export DB_NAME=incidents_db
export SSL=disable
export OPENAI_API_KEY=<your-openai-api-key>
export KO_DATA_PATH=./kodata

go run main.go
```

### 3. Build OCI image with KO (no Dockerfile)

```bash
# KO compiles the Go binary and packages it into a Distroless container image
KO_DOCKER_REPO=<your-registry>/incident-response \
  ko build --bare -t v1 .
```

---

## Kubernetes Cluster Setup

### Create cluster

```bash
ksctl create-cluster azure --name=application --version=1.29
ksctl switch-cluster --provider azure --region eastus --name application
export KUBECONFIG="/Users/<you>/.ksctl/kubeconfig"
```

### Install cert-manager (TLS)

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.15.3/cert-manager.yaml

# Enable Gateway API support
kubectl edit deployment cert-manager -n cert-manager
# Add flag:  --enable-gateway-api

kubectl rollout restart deployment cert-manager -n cert-manager
```

### Install Prometheus + Grafana (kube-prometheus-stack)

```bash
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
helm install kube-prometheus-stack prometheus-community/kube-prometheus-stack \
  --namespace monitoring --create-namespace

# Get Grafana admin password
kubectl get secret --namespace monitoring kube-prometheus-stack-grafana \
  -o jsonpath="{.data.admin-password}" | base64 --decode ; echo

# Access Grafana locally
kubectl port-forward svc/kube-prometheus-stack-grafana 3000:80 -n monitoring
```

### Install NGINX Gateway Fabric (Kubernetes Gateway API)

```bash
kubectl kustomize \
  "https://github.com/nginxinc/nginx-gateway-fabric/config/crd/gateway-api/standard?ref=v1.3.0" \
  | kubectl apply -f -

helm install ngf oci://ghcr.io/nginxinc/charts/nginx-gateway-fabric \
  --create-namespace -n nginx-gateway
```

### Install CloudNativePG (PostgreSQL operator)

```bash
kubectl apply --server-side -f \
  https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.23/releases/cnpg-1.23.1.yaml

# Create the PostgreSQL cluster (3 replicas for HA)
cat << EOF | kubectl apply -f -
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: my-postgresql
  namespace: default
spec:
  instances: 3
  storage:
    size: 1Gi
  bootstrap:
    initdb:
      database: incidents_database
      owner: incidents_user
      secret:
        name: my-postgresql-credentials
EOF

# Create the database credentials secret
kubectl create secret generic my-postgresql-credentials \
  --from-literal=password='new_password' \
  --from-literal=username='incidents_user' \
  --dry-run=client -o yaml | kubectl apply -f -
```

### Create the incidents table

```bash
kubectl port-forward my-postgresql-1 5432:5432

PGPASSWORD='new_password' psql -h 127.0.0.1 -U incidents_user -d incidents_database -c "
CREATE TABLE incidents (
    id         SERIAL PRIMARY KEY,
    pod_name   TEXT NOT NULL,
    namespace  TEXT NOT NULL,
    status     TEXT,
    analysis   TEXT,
    created_at TIMESTAMP
);
"
```

### Create Kubernetes Secrets

```bash
# PostgreSQL credentials used by the app
cat << EOF | kubectl apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: postgresql-credentials
type: Opaque
data:
  password: bmV3X3Bhc3N3b3Jk   # base64("new_password")
  username: aW5jaWRlbnRzX3VzZXI=  # base64("incidents_user")
EOF

# OpenAI API key
kubectl create secret generic openai-credentials \
  --from-literal=api-key=<YOUR_OPENAI_API_KEY>
```

### Deploy the application (ArgoCD syncs this automatically)

```bash
kubectl apply -f deploy/deploy.yaml
```

### Install ArgoCD

```bash
kubectl create namespace argocd
kubectl apply -n argocd -f \
  https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml

# Allow insecure HTTP (gateway handles TLS termination)
kubectl patch configmap argocd-cmd-params-cm -n argocd \
  --patch '{"data":{"server.insecure":"true"}}'

kubectl rollout restart deployment argocd-server -n argocd

# Get admin password
kubectl get secret --namespace argocd argocd-initial-admin-secret \
  -o jsonpath="{.data.password}" | base64 --decode ; echo

# Expose ArgoCD through the Gateway
kubectl apply -f route-argo.yaml
kubectl apply -f referencegrant
```

---

## Load Testing with k6

Simulates 100 concurrent engineers submitting incident analysis requests to measure:
- API response time and throughput
- Error rate under load
- Kubernetes pod CPU / memory usage (visible in Grafana)
- HPA auto-scaling behaviour

```bash
k6 run load.js
```

The test POSTs to `/analyze` with a sample pod payload and validates that each response contains an `analysis` field.

---

## Observability

- **Prometheus** scrapes `/metrics` every 15 s — counters for `incident_analyze_requests_total`, `incident_history_requests_total`, `http_requests_total`
- **Grafana** dashboards visualise request rates, API latency, pod resource usage, and HPA scaling
- **ServiceMonitor** (`servicemonitor.yaml`) registers the app with the kube-prometheus-stack operator
