The file `main.go` contains an HTTP handler that looks up users by name from a SQL database. The current implementation is vulnerable to SQL injection because it uses string interpolation to build the SQL query.

Fix the SQL injection vulnerability by converting all SQL queries to use parameterized queries. Do not change the HTTP interface or response format. The existing tests must continue to pass.
