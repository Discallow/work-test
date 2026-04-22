import { useState } from "react"; // React's built-in capability to remember data between renders
import "./App.css";

function App() {
  const [query, setQuery] = useState(""); // To remember what the user is typing in the search bar
  const [employees, setEmployees] = useState([]); // To store the list of people returned from the DB
  const [loading, setLoading] = useState(false); // To track if the "Loading..." message should be visible
  const [error, setError] = useState(""); // To hold any error messages if the fetch fails
  const [hasSearched, setHasSearched] = useState(false); // To remember if the user has clicked "Search" at least once

  async function handleSearch(e) {
    e.preventDefault(); // to prevent the browser from doing its default behavior and don't let it reload the page. React controls everything

    try {
      setLoading(true);
      setError("");
      setHasSearched(true);

      const response = await fetch(
        `/employees?q=${encodeURIComponent(query.trim())}`
      );

      if (!response.ok) {
        const text = await response.text();
        throw new Error(text || "Failed to fetch employees");
      }

      const data = await response.json();
      setEmployees(data);
    } catch (err) {
      setEmployees([]);
      setError(err.message || "Could not fetch employees.");
    } finally { // Whether the search succeeded OR failed, this ensures loading becomes false at the very end of the process.
      setLoading(false);
    }
  }

  return (
    <main className="app">
      <h1>Employees</h1>

      <form className="search-form" onSubmit={handleSearch}>
        <input
          type="text"
          placeholder="Search employee name or department"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          className="search-input"
        />
        <button type="submit" className="search-button">
          Search
        </button>
      </form>

      {!hasSearched && <p>Use the search box to find employees.</p>}

      {loading && <p>Loading...</p>}
      {error && <p className="error">{error}</p>}

      {hasSearched && !loading && !error && employees.length === 0 && (
        <p>No employees found.</p>
      )}

      {hasSearched && employees.length > 0 && (
        <table className="employees-table">
          <thead>
            <tr>
              <th>Department</th>
              <th>Name</th>
            </tr>
          </thead>
          <tbody>
            {employees.map((employee, index) => (
              <tr key={`${employee.name}-${employee.department}-${index}`}>
                <td>{employee.department}</td>
                <td>{employee.name}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </main>
  );
}

export default App;