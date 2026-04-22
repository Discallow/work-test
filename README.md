# Employees App — Architecture & Technical Walkthrough

## 1. Overview

This project is a small full-stack employee search system composed of four main parts:

* **Frontend**: React + Vite
* **Backend**: Go HTTP API
* **Database**: PostgreSQL initialized with employee data from CSV
* **Monitoring**: Prometheus, Grafana, and `postgres_exporter`

At a high level, the request flow is:

**User → Frontend → Backend → PostgreSQL**

And the observability flow is:

**Backend `/metrics` + `postgres_exporter` → Prometheus → Grafana**

---

## 2. Container Architecture

The system is orchestrated with Docker Compose.

### Services

* **postgres**

  * Built from `./database`
  * Stores the employee table and seeded CSV data
  * Uses a persistent volume: `pgdata`

* **backend**

  * Built from `./backend`
  * Exposes the Go API on port `8080`
  * Connects to PostgreSQL using environment variables

* **frontend**

  * Built from `./frontend`
  * Runs the Vite dev server on port `5173`
  * Proxies API calls to the backend container

* **postgres_exporter**

  * Built from `./monitoring/postgres_exporter`
  * Exposes PostgreSQL metrics for Prometheus

* **prometheus**

  * Built from `./monitoring/prometheus`
  * Scrapes metrics from itself, the backend, and `postgres_exporter`

* **grafana**

  * Built from `./monitoring/grafana`
  * Connects to Prometheus as its datasource
  * Stores its own data in the `grafana_data` volume

### Network

All containers are attached to the same custom bridge network:

* **network**

This is what allows containers to reach each other by service name, for example:

* `backend:8080`
* `postgres:5432`
* `prometheus:9090`

### Persistence

Two named volumes are used:

* `pgdata` for PostgreSQL data
* `grafana_data` for Grafana state

---

## 3. Database Layer

### Files

* `database/Dockerfile`
* `database/tools/init.sql`
* `database/tools/employeeTable.csv`

### Dockerfile

```dockerfile
FROM postgres:15

COPY tools/init.sql /docker-entrypoint-initdb.d/
COPY tools/employeeTable.csv /docker-entrypoint-initdb.d/

EXPOSE 5432
```

This image extends the official PostgreSQL image and copies initialization assets into `/docker-entrypoint-initdb.d/`.

That directory is special in the Postgres image:

* SQL scripts placed there are executed automatically on first database initialization

### SQL Initialization

```sql
CREATE TABLE employees (
    department TEXT NOT NULL,
    name TEXT NOT NULL
);

COPY employees(department, name)
FROM '/docker-entrypoint-initdb.d/employeeTable.csv'
DELIMITER ','
CSV HEADER;
```

### What it does

1. Creates the `employees` table
2. Imports rows from the CSV file into the table

### Design relevance

This gives the application an immediately usable dataset without requiring manual inserts.

## 4. Backend Layer (Go API)

### Files

* `backend/main.go`
* `backend/Dockerfile`

### Purpose

The backend exposes three endpoints:

* `GET /health`
* `GET /employees?q=<search>`
* `GET /metrics`

It is responsible for:

* connecting to PostgreSQL
* querying employees
* returning JSON results
* exposing Prometheus metrics

---

## 5. Backend Structure

The backend is organized around a few important concepts:

* **model**: `Employee`
* **repository interface**: `EmployeeRepository`
* **SQL repository implementation**: `SQLEmployeeRepository`
* **HTTP handlers**: `EmployeeHandler`
* **middleware**: `metricsMiddleware`

This is a clean structure because HTTP logic, data access logic, and monitoring logic are separated.

---

## 6. Data Model

```go
type Employee struct {
	Department string `json:"department"`
	Name       string `json:"name"`
}
```

This struct represents one employee record.


## 7. Repository Pattern and DI

### Interface

```go
type EmployeeRepository interface {
	ExecuteQuery(ctx context.Context, search string) ([]Employee, error)
}
```

### Concrete implementation

```go
type SQLEmployeeRepository struct {
	db *sql.DB
}
```

### Why this matters

This is an example of **dependency injection** and **abstraction**.

Instead of having the handler talk directly to PostgreSQL query code, it depends on the `EmployeeRepository` interface.

That gives several advantages:

* the handler is decoupled from SQL details
* the repository can be swapped in tests
* the code is easier to reason about

This commented section in the code shows that clearly:

```go
/* type FakeRepo struct{}

func (f *FakeRepo) ExecuteQuery(ctx context.Context, search string) ([]Employee, error) {
	return []Employee{
		{Name: "Alice", Department: "IT"},
	}, nil
} */
```

That fake implementation could be injected into the handler without changing the handler itself.

---

## 8. SQL Query Function

### Function

```go
func (r *SQLEmployeeRepository) ExecuteQuery(ctx context.Context, search string) ([]Employee, error)
```

### What it does

This function executes a case-insensitive search against the `employees` table.

Relevant SQL:

```sql
SELECT department, name
FROM employees
WHERE name ILIKE '%' || $1 || '%'
   OR department ILIKE '%' || $1 || '%'
```

### Why `ILIKE`

`ILIKE` is PostgreSQL’s case-insensitive version of `LIKE`.

So these searches behave similarly:

* `it`
* `IT`
* `It`

### Why `$1`

This is a parameterized query placeholder.

That is important because it avoids raw string interpolation into SQL and prevents SQL injection in this query path.

If it was: **query := "SELECT ... WHERE name ILIKE '%" + search + "%'"** and the user inputs **john' OR 1=1 --**, query becomes **WHERE name ILIKE '%john' OR 1=1 --%'**, which is SQL Injection.

### Why `QueryContext`

```go
rows, err := r.db.QueryContext(ctx, query, search)
```

This allows the database query to respect request cancellation and timeouts.

That means if:

* the client disconnects
* the handler times out
* the context is canceled

then the query can be canceled too.

### Flow

1. Build query
2. Execute it with context
3. Iterate through rows
4. Scan each row into `Employee`
5. Append to slice
6. Check `rows.Err()`
7. Return result slice

### Relevance

This is the core backend business function.

---

## 9. HTTP Handler Layer

### Struct

```go
type EmployeeHandler struct {
	repo EmployeeRepository
	db   *sql.DB
}
```

This handler receives its dependencies from `main()`.

### Why this is DI

The handler does not create its own repository or database connection.

Instead, they are injected from outside:

```go
repo := &SQLEmployeeRepository{db: db}
handlers := &EmployeeHandler{
	repo: repo,
	db:   db,
}
```

This keeps initialization logic in `main()` and keeps handler logic focused on request handling.

---

## 10. `ListEmployees` Endpoint

### Route

* `GET /employees?q=<search>`

### Function responsibilities

* validate method
* read query parameter
* create request timeout
* call repository
* handle errors
* return JSON

### Query extraction

```go
search := r.URL.Query().Get("q")
```

This reads the `q` query parameter from the URL.

Example:

```text
/employees?q=it
```

Then `search == "it"`.

### Context timeout

```go
ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
defer cancel()
```

This means the handler gives itself a maximum of 2 seconds for the operation.

Using `r.Context()` is important because it inherits request cancellation from the HTTP request.

So if the client disconnects, the context is canceled too.

### Error handling

```go
switch ctx.Err() {
case context.DeadlineExceeded:
	http.Error(w, "request timeout", http.StatusGatewayTimeout)
case context.Canceled:
	return
default:
	...
}
```

This distinguishes between:

* request timeout
* canceled request
* other query failures

### Response encoding

```go
json.NewEncoder(w).Encode(employees)
```

This serializes the result into JSON.

### Relevance

This is the main application endpoint used by the frontend.

---

## 11. `CheckHealth` Endpoint

### Route

* `GET /health`

### What it does

It checks whether the application can still reach the database.

### Flow

1. Ensure method is GET
2. Create 2-second timeout
3. Ping DB with `PingContext`
4. Return `503` if DB is unreachable
5. Return `200 OK` with `OK` body otherwise

### Why `PingContext`

This is the context-aware version of `Ping()`.

It ensures the check does not block forever.

### Operational relevance

This endpoint is useful for:

* manual checks
* monitoring checks
* container/platform health integration

---

## 12. Database Connection Pool

### Function

```go
func setupConnectionPool(db *sql.DB)
```

### Configuration

```go
db.SetMaxOpenConns(25)
db.SetMaxIdleConns(25)
db.SetConnMaxIdleTime(30 * time.Minute)
db.SetConnMaxLifetime(5 * time.Minute)
```

### What these do

* `SetMaxOpenConns(25)`

  * limits total concurrent DB connections

* `SetMaxIdleConns(25)`

  * keeps idle connections available for reuse

* `SetConnMaxIdleTime(30m)`

  * closes DB connections that stay unused too long

* `SetConnMaxLifetime(5m)`

  * recycles connections after a maximum lifetime

### Relevance

This protects the application from uncontrolled connection growth and helps manage DB resources more predictably.

---

## 13. Environment Loading

### Function

```go
func mustGetenv(key string) string
```

### Purpose

It reads required environment variables and fails fast if one is missing.

### Why it matters

This is useful because the app cannot start correctly without its DB configuration.

Instead of failing later with a vague error, it crashes immediately with a clear message.

---

## 14. Prometheus Metrics in the Backend

The backend exposes its own metrics through:

* `GET /metrics`

### Defined metrics

```go
var (
	httpRequestsTotal = prometheus.NewCounterVec(...)
	httpRequestDuration = prometheus.NewHistogramVec(...)
)
```

These are package-level variables initialized at program startup, before `main()` runs.

### Counter

```go
http_requests_total
```

Labels:

* `path`
* `method`
* `status`

This metric answers:

* how many requests happened
* to which endpoint
* with which method
* with which response code

### Histogram

```go
http_request_duration_seconds
```

Labels:

* `path`
* `method`

This metric answers:

* how long requests take
* average latency
* percentile latency (p95, p99)

### Why registration is needed

These metrics are only created in memory at declaration time. They become exposed to Prometheus only after registration:

```go
prometheus.MustRegister(httpRequestsTotal)
prometheus.MustRegister(httpRequestDuration)
```

---

## 15. `statusRecorder` and Why It Exists

### Struct

```go
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}
```

---

## 16. Metrics Middleware

### Function

```go
func metricsMiddleware(path string, next http.HandlerFunc) http.HandlerFunc
```

### What it does

This middleware wraps a handler and records:

* request count
* request duration
* response status code

### Flow

1. Record start time
2. Wrap `ResponseWriter` with `statusRecorder`
3. Execute the real handler
4. Compute request duration
5. Extract final status code
6. Increment counter
7. Observe duration in histogram

### Relevance

This is the key monitoring addition on the backend side.

It turns the backend into something Prometheus can meaningfully observe.

---

## 17. Router and Endpoints

### Router

```go
router := http.NewServeMux()
```

This is the HTTP router.

It maps URL paths to handler functions.

### Registered routes

```go
router.HandleFunc("/health", metricsMiddleware("/health", handlers.CheckHealth))
router.HandleFunc("/employees", metricsMiddleware("/employees", handlers.ListEmployees))
router.Handle("/metrics", promhttp.Handler())
```

### Why this is important

* `/health` and `/employees` are wrapped with the metrics middleware
* `/metrics` is handled by Prometheus’s standard handler

### Why `Handle` for `/metrics`

`promhttp.Handler()` returns an `http.Handler`, not an `http.HandlerFunc`, so `Handle()` is the correct method.

---

## 18. HTTP Server Configuration

```go
server := &http.Server{
	Addr:              ":8080",
	Handler:           router,
	ReadHeaderTimeout: 5 * time.Second,
	ReadTimeout:       10 * time.Second,
	WriteTimeout:      10 * time.Second,
	IdleTimeout:       60 * time.Second,
}
```

### Why set these instead of defaults

Because the defaults are effectively unlimited for several behaviors.

These timeouts protect the server from:

* slow or malicious clients
* hanging reads
* hanging writes
* idle connections consuming resources too long

### Important distinction

These HTTP timeouts are unrelated to the DB pool idle time.

* HTTP `IdleTimeout` controls client → backend connections
* `SetConnMaxIdleTime` controls backend → database connections

Different layer, different purpose.

To test it: 

* successful request: printf 'GET /health HTTP/1.1\r\nHost: localhost\r\n\r\n' | nc -v localhost 8080 
* request closed before response: { printf 'GET /employees HTTP/1.1\r\n'; sleep 6; printf 'Host: localhost\r\n\r\n'; } | nc -v localhost 8080

---

## 19. Backend Dockerfile

```dockerfile
FROM golang:1.24

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o server

EXPOSE 8080

CMD ["./server"]
```

### What it does

1. Starts from the Go 1.24 image
2. Sets `/app` as working directory
3. Copies dependency files first
4. Downloads dependencies
5. Copies source code
6. Exposes port 8080
7. Runs the app

### Relevance

This is a straightforward development-oriented container.

For production, a compiled binary plus a smaller runtime image would usually be preferred, but this version is simple and effective for learning and local environments.

---

## 20. Frontend Layer

### Files

* `frontend/vite.config.js`
* `frontend/index.html`
* `frontend/src/main.jsx`
* `frontend/src/App.jsx`
* `frontend/Dockerfile`

### Purpose

The frontend provides the user interface that lets a user search employees by name or department.

---

## 21. Vite Configuration

### File

`vite.config.js`

```js
import react from "@vitejs/plugin-react";

export default {
  plugins: [react()],
  server: {
    host: "0.0.0.0",
    port: 5173,
    proxy: {
      "/employees": {
        target: "http://backend:8080",
        changeOrigin: true,
      },
      "/health": {
        target: "http://backend:8080",
        changeOrigin: true,
      },
    },
  },
};
```

### What this file is

This is Vite’s configuration file.

It controls how the development server behaves.

### Important lines

* `plugins: [react()]`

  * enables React support and hot refresh

* `host: "0.0.0.0"`

  * makes the Vite dev server reachable from outside the container

* `port: 5173`

  * Vite dev server port

* `proxy`

  * forwards frontend API requests to the backend container

### Why proxy matters

If the frontend calls `/employees`, Vite forwards it to:

* `http://backend:8080/employees`


### Important operational note

This proxy is a development-time Vite feature. In a production deployment, NGINX could be used instead for example.

---

## 22. Frontend Entry Point

### `index.html`

This is the HTML shell Vite serves.

Relevant lines:

```html
<div id="root"></div>
<script type="module" src="/src/main.jsx"></script>
```

### Meaning

* `#root` is the DOM node React mounts into
* `main.jsx` is the JavaScript entry point

---

## 23. React Bootstrap File

### `main.jsx`

```jsx
ReactDOM.createRoot(document.getElementById("root")).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
```

### What it does

* finds the root DOM element
* creates a React root
* renders the `App` component into it

### Why `React.StrictMode`

This enables additional development checks and warnings.

---

## 24. Main Frontend Component

### `App.jsx`

This is the entire UI logic.

### State variables

```jsx
const [query, setQuery] = useState("");
const [employees, setEmployees] = useState([]);
const [loading, setLoading] = useState(false);
const [error, setError] = useState("");
const [hasSearched, setHasSearched] = useState(false);
```

---

## 25. Frontend Request Flow

### Function

```jsx
async function handleSearch(e)
```

### What happens

1. Prevent form reload
2. Set loading state
3. Reset previous error
4. Mark search as started
5. Send request to `/employees?q=<query>`
6. If response is not OK, read text and throw error
7. Parse JSON on success
8. Store employees in state
9. On failure, clear employees and show error
10. Finally, stop loading

### Important line

```jsx
const response = await fetch(
  `/employees?q=${encodeURIComponent(query.trim())}`
);
```

### Why `encodeURIComponent`

This safely encodes user input so special characters do not break the URL.

### Why `query.trim()`

Removes leading/trailing spaces before searching.

### How this connects to Vite proxy

The frontend is not calling `http://backend:8080` directly.

It calls `/employees`, and Vite proxies that request to the backend container.

---

## 26. Frontend Rendering Logic

The UI conditionally renders based on state:

* before search:

  * “Use the search box to find employees.”

* while loading:

  * “Loading...”

* on error:

  * error message

* after a search with no results:

  * “No employees found.”

* after successful search with data:

  * employees table

---

## 27. Frontend Dockerfile

```dockerfile
FROM node:22-alpine

WORKDIR /app

COPY package*.json ./
RUN npm install
COPY . .

EXPOSE 5173
CMD ["npm", "run", "dev"]
```

### What it does

1. Starts from Node 22 Alpine
2. Sets working directory
3. Installs dependencies
4. Copies source code
5. Exposes Vite port
6. Starts the Vite dev server

### Relevance

This is a development container. It intentionally runs `npm run dev`, not a production build.

---

## 28. Monitoring Layer

### Components

* backend Prometheus metrics endpoint
* `postgres_exporter`
* Prometheus
* Grafana

### Monitoring responsibilities

* backend request count and latency
* PostgreSQL exporter metrics
* Prometheus scraping and storage
* Grafana visualization

---

## 29. Prometheus

### Dockerfile

```dockerfile
FROM prom/prometheus:v3.11.2

COPY prometheus.yml /etc/prometheus/prometheus.yml

CMD ["--config.file=/etc/prometheus/prometheus.yml"]
```

### Configuration

```yaml
global:
  scrape_interval: 15s
  evaluation_interval: 15s

scrape_configs:
  - job_name: 'prometheus'
    scheme: http
    static_configs:
      - targets: ['prometheus:9090']
  
  - job_name: 'postgres_exporter'
    scheme: http
    static_configs:
      - targets: ['postgres_exporter:9187']
    
  - job_name: 'backend'
    scheme: http
    static_configs:
      - targets: ['backend:8080']
```

### What it does

Prometheus scrapes:

* itself
* `postgres_exporter`
* backend `/metrics`

### Why backend target works without explicit `/metrics`

Prometheus defaults to scraping the `/metrics` path unless `metrics_path` is changed.

So `backend:8080` means:

* `http://backend:8080/metrics`

---

## 30. `postgres_exporter`

### Dockerfile

```dockerfile
FROM prometheuscommunity/postgres-exporter:v0.19.1
```

### Role

This exporter connects to PostgreSQL and exposes database metrics in Prometheus format.

### What it gives

Typical DB-side visibility includes things such as:

* exporter up/down
* DB reachability
* connection stats
* PostgreSQL internal metrics exposed by the exporter

### Important distinction

This exporter does **not** tell how many API requests the backend is receiving.

That is why backend instrumentation was needed separately.

---

## 31. Grafana

### Dockerfile

```dockerfile
FROM grafana/grafana:13.0.1

COPY provisioning /etc/grafana/provisioning
COPY dashboards /var/lib/grafana/dashboards
```

### Datasource provisioning

```yaml
apiVersion: 1

datasources:
  - name: Prometheus
    type: prometheus
    access: proxy
    url: http://prometheus:9090
    isDefault: true
    editable: true
```

### What this does

It preconfigures Grafana to use Prometheus as its default datasource.

### Relevance

This makes Grafana ready to query Prometheus without manual datasource setup.

---

## 32. End-to-End Application Flow

### Search request flow

1. User opens frontend on port `5173`
2. User types a search term and submits form
3. React calls:

   * `/employees?q=<term>`
4. Vite proxies that request to:

   * `http://backend:8080/employees?q=<term>`
5. Backend handler creates a timeout context
6. Handler calls repository
7. Repository runs SQL query on PostgreSQL
8. Results are scanned into `[]Employee`
9. Backend returns JSON
10. Frontend renders table

### Health flow

1. User or system calls `/health`
2. Backend pings DB with timeout
3. Returns `OK` or `503 DB unreachable`

### Monitoring flow

1. Backend updates custom Prometheus metrics on each request
2. Backend exposes them at `/metrics`
3. `postgres_exporter` exposes DB metrics
4. Prometheus scrapes both on a schedule
5. Grafana queries Prometheus and renders dashboards

---

## 33. Important Design Decisions

### 1. Dependency Injection

Used in the backend so that handlers receive dependencies instead of creating them.

This improves:

* testability
* separation of concerns
* flexibility

### 2. Context usage

Used in both startup and request lifecycle.

This improves:

* timeout control
* cancellation propagation
* resilience against hanging operations

### 3. Middleware for metrics

Used so monitoring logic is not duplicated inside each handler.

This keeps handler code focused on business logic.

### 4. Vite proxy

Used so frontend code can call relative API paths without CORS complexity during development.

---

## 34. Strengths of the Current Architecture

* clear separation between FE, BE, DB, and monitoring
* simple request flow
* backend already instrumented for Prometheus
* containerized and easy to run with Compose
* seeded database allows instant testing
* use of context and timeouts is a strong design choice
* repository abstraction makes the Go code more maintainable

---

## 35. Practical Limitations and Notes

### Frontend container

The frontend runs the Vite dev server in Docker. This is convenient for development, but production would usually use a built static bundle served by NGINX or similar.

---

## 36. Summary

* The **frontend** provides a simple employee search UI
* The **backend** exposes search, health, and metrics endpoints
* The **database** stores employee records and is seeded automatically
* The **monitoring stack** collects and visualizes both backend and database metrics

The most important technical ideas in the codebase are:

* dependency injection in the backend
* context-based timeouts and cancellation
* middleware-based Prometheus instrumentation
* Vite proxying for frontend-to-backend development traffic

Together, these choices make the system relatively simple, but also much cleaner and more production-aware than a minimal demo app.
