#!/bin/sh
# Create database `shop` (if missing) and write load against it in a compose service while a backup runs (SC-004).
# Usage: testdata/load.sh <pg-a|mysql-a|mssql-a> <seconds>
# Prints the number of failed writes at the end; expected 0.
set -u
svc=$1; secs=${2:-30}
dc="docker compose -f $(dirname "$0")/compose.yaml exec -T $svc"
case $svc in
  pg-*)    sql() { $dc psql -U postgres -d shop -qc "$1" >/dev/null 2>&1; }
           $dc psql -U postgres -qc "CREATE DATABASE shop" >/dev/null 2>&1
           create="CREATE TABLE IF NOT EXISTS orders (id INT, note VARCHAR(50))" ;;
  mysql-*) sql() { $dc mysql -uroot -pDumpling-Test-1 shop -e "$1" >/dev/null 2>&1; }
           $dc mysql -uroot -pDumpling-Test-1 -e "CREATE DATABASE IF NOT EXISTS shop" >/dev/null 2>&1
           create="CREATE TABLE IF NOT EXISTS orders (id INT, note VARCHAR(50))" ;;
  mssql-*) sql() { $dc /opt/mssql-tools18/bin/sqlcmd -C -S localhost -U sa -P Dumpling-Test-1 -d shop -b -Q "$1" >/dev/null 2>&1; }
           $dc /opt/mssql-tools18/bin/sqlcmd -C -S localhost -U sa -P Dumpling-Test-1 -b -Q "IF DB_ID('shop') IS NULL CREATE DATABASE shop" >/dev/null 2>&1
           create="IF OBJECT_ID('orders') IS NULL CREATE TABLE orders (id INT, note VARCHAR(50))" ;;
  *) echo "unknown service $svc" >&2; exit 2 ;;
esac
sql "CREATE TABLE IF NOT EXISTS orders (id INT, note VARCHAR(50))" 2>/dev/null || \
  sql "IF OBJECT_ID('orders') IS NULL CREATE TABLE orders (id INT, note VARCHAR(50))"
end=$(( $(date +%s) + secs )); i=0; failed=0
while [ "$(date +%s)" -lt "$end" ]; do
  i=$((i+1))
  sql "INSERT INTO orders VALUES ($i, 'load')" || failed=$((failed+1))
done
echo "writes=$i failed=$failed"
