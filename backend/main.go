package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Employee struct {
	Department string `json:"department"`
	Name       string `json:"name"`
}

type EmployeeRepository interface {
	ExecuteQuery(ctx context.Context, search string) ([]Employee, error)
}

type SQLEmployeeRepository struct {
	db *sql.DB
}

// ExecuteQuery runs a case-insensitive search against the employees table
// and returns matching employees by name or department.
//
// Parameters:
// - ctx: request-scoped context used for timeout/cancellation.
// - search: string used to filter employees (matched with ILIKE).
//
// Returns:
// - []Employee: slice of matching employees (empty if none found).
// - error: non-nil if the query or row scanning fails.
//
// Flow:
// 1. Build SQL query with ILIKE filters using a parameterized placeholder.
// 2. Execute query with context (QueryContext).
// 3. Iterate over rows and scan into Employee structs.
// 4. Collect results into a slice.
// 5. Check for iteration errors (rows.Err).
// 6. Return slice and error (if any).
func (r *SQLEmployeeRepository) ExecuteQuery(ctx context.Context, search string) ([]Employee, error) {

	query := `
		SELECT department, name
		FROM employees
		WHERE name ILIKE '%' || $1 || '%'
			OR department ILIKE '%' || $1 || '%'
	`
	rows, err := r.db.QueryContext(ctx, query, search)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var employees = make([]Employee, 0)
	for rows.Next() {
		var e Employee
		if err := rows.Scan(&e.Department, &e.Name); err != nil {
			return nil, err
		}
		employees = append(employees, e)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return employees, nil
}

type EmployeeHandler struct {
	repo EmployeeRepository
	db   *sql.DB
}

// ListEmployees handles GET /employees requests and returns employees
// filtered by a search query parameter.
//
// Parameters:
// - w: http.ResponseWriter used to write the HTTP response.
// - r: *http.Request containing query params and request context.
//
// Returns:
// - No direct return values; writes HTTP response (JSON or error).
//
// Flow:
// 1. Validate HTTP method (GET only).
// 2. Extract "q" query parameter and validate it.
// 3. Create a context with timeout (2s).
// 4. Call repository ExecuteQuery with context and search term.
// 5. Handle timeout, cancellation, or query errors.
// 6. Set JSON response header.
// 7. Encode and write employees slice to response.
func (h *EmployeeHandler) ListEmployees(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	search := r.URL.Query().Get("q")

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	employees, err := h.repo.ExecuteQuery(ctx, search)
	if err != nil {
		switch ctx.Err() {
		case context.DeadlineExceeded:
			http.Error(w, "request timeout", http.StatusGatewayTimeout)
			return
		case context.Canceled:
			return
		default:
			log.Printf("query failed: q=%q err=%v", search, err)
			http.Error(w, "query failed", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(employees); err != nil {
		http.Error(w, "json encode failed", http.StatusInternalServerError)
		return
	}
}

// CheckHealth verifies that the application and database are reachable
// and responds with a simple OK status.
//
// Parameters:
// - w: http.ResponseWriter used to send the HTTP response.
// - r: *http.Request providing request context.
//
// Returns:
// - No direct return values; writes HTTP status and message.
//
// Flow:
// 1. Validate HTTP method (GET only).
// 2. Create a context with timeout (2s).
// 3. Ping the database using PingContext.
// 4. If DB is unreachable, return 503 error.
// 5. Otherwise, return 200 OK with "OK" body.
func (h *EmployeeHandler) CheckHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if h.db != nil {
		if err := h.db.PingContext(ctx); err != nil {
			http.Error(w, "DB unreachable", http.StatusServiceUnavailable)
			return
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

// setupConnectionPool configures database connection pool limits
// to control concurrency and resource usage.
//
// Parameters:
// - db: *sql.DB connection pool instance to configure.
//
// Returns:
// - No return value; modifies the db pool settings in-place.
//
// Flow:
// 1. Set maximum number of open connections.
// 2. Set maximum number of idle connections.
// 3. Configure idle connection timeout.
// 4. Configure maximum lifetime for each connection.
func setupConnectionPool(db *sql.DB) {

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)
	db.SetConnMaxIdleTime(30 * time.Minute)
	db.SetConnMaxLifetime(5 * time.Minute)
}

// mustGetenv retrieves an environment variable or terminates the program
// if the variable is not set.
//
// Parameters:
// - key: name of the environment variable to retrieve.
//
// Returns:
// - string: value of the environment variable.
//
// Flow:
// 1. Read environment variable using os.Getenv.
// 2. If empty, log fatal error and exit program.
// 3. Otherwise, return the value.
func mustGetenv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("missing required environment variable: %s", key)
	}
	return v
}

var (
	httpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests.",
		},
		[]string{"path", "method", "status"},
	)
	httpRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Duration of HTTP requests in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"path", "method"},
	)
)

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(statusCode int) {
	r.statusCode = statusCode
	r.ResponseWriter.WriteHeader(statusCode)
}

// metricsMiddleware wraps an HTTP handler to collect Prometheus metrics
// such as request count and request duration.
//
// Parameters:
//   - path: string representing the logical route path (e.g. "/employees").
//     This is used as a label to avoid high cardinality from dynamic URLs.
//   - next: http.HandlerFunc representing the actual handler to be executed.
//
// Returns:
//   - http.HandlerFunc: a wrapped handler that records metrics before and
//     after executing the original handler.
//
// Flow:
//  1. Record the start time of the request.
//  2. Wrap the ResponseWriter with a statusRecorder to capture the status code.
//  3. Call the original handler (next) with the wrapped ResponseWriter.
//  4. After the handler finishes:
//     a. Calculate the request duration.
//     b. Extract the HTTP status code from the recorder.
//  5. Increment the httpRequestsTotal counter with labels (path, method, status).
//  6. Record the request duration in the httpRequestDuration histogram
//     with labels (path, method).
//
// Notes:
//   - The status code defaults to 200 if the handler does not explicitly call WriteHeader.
//   - The path is passed explicitly to avoid including query parameters or dynamic
//     values, which would increase label cardinality and impact Prometheus performance.
//   - This middleware should wrap all handlers you want to monitor.
func metricsMiddleware(path string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		recorder := &statusRecorder{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}

		next(recorder, r)

		duration := time.Since(start).Seconds()
		status := strconv.Itoa(recorder.statusCode)

		httpRequestsTotal.WithLabelValues(path, r.Method, status).Inc()
		httpRequestDuration.WithLabelValues(path, r.Method).Observe(duration)
	}
}

/* type FakeRepo struct{}

func (f *FakeRepo) ExecuteQuery(ctx context.Context, search string) ([]Employee, error) {
	return []Employee{
		{Name: "Alice", Department: "IT"},
	}, nil
} */

// main initializes the database connection, configures the application,
// registers HTTP routes, and starts the HTTP server.
//
// Parameters:
// - None.
//
// Returns:
// - Does not return; exits on fatal error.
//
// Flow:
//  1. Read required environment variables (DB config).
//  2. Build PostgreSQL connection string.
//  3. Open database connection.
//  4. Configure connection pool settings.
//  5. Verify DB connectivity with PingContext.
//  6. Initialize repository and handler structs.
//  7. Create an HTTP router (ServeMux), register routes, and map each URL
//     path (e.g. /health, /employees) to its corresponding handler.
//  8. Configure the HTTP server and set time limits on client connections
//     so the server does not hang or waste resources on slow or stuck requests.
//  9. Start server and listen for incoming requests.
func main() {
	sslMode := os.Getenv("DB_SSLMODE")
	if sslMode == "" {
		sslMode = "disable"
	}

	connStr := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		mustGetenv("DB_HOST"),
		mustGetenv("DB_PORT"),
		mustGetenv("DB_USER"),
		mustGetenv("DB_PASSWORD"),
		mustGetenv("DB_NAME"),
		sslMode,
	)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	setupConnectionPool(db)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // Background() is a base context
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatal(err)
	}

	repo := &SQLEmployeeRepository{db: db}
	handlers := &EmployeeHandler{
		repo: repo,
		db:   db,
	}

	/* 	handlers := &EmployeeHandler{
		repo: &FakeRepo{},
	} */

	router := http.NewServeMux()
	router.HandleFunc("/health", metricsMiddleware("/health", handlers.CheckHealth))
	router.HandleFunc("/employees", metricsMiddleware("/employees", handlers.ListEmployees))
	router.Handle("/metrics", promhttp.Handler())
	prometheus.MustRegister(httpRequestsTotal)
	prometheus.MustRegister(httpRequestDuration)

	server := &http.Server{
		Addr:              ":8080",
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}
