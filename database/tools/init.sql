CREATE TABLE employees (
    department TEXT NOT NULL,
    name TEXT NOT NULL
);

COPY employees(department, name)
FROM '/docker-entrypoint-initdb.d/employeeTable.csv'
DELIMITER ','
CSV HEADER;