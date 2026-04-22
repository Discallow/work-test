FROM postgres:15

COPY tools/init.sql /docker-entrypoint-initdb.d/
COPY tools/employeeTable.csv /docker-entrypoint-initdb.d/

EXPOSE 5432