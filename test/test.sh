echo "Hello World"
echo "Script path: $0"
echo "Argument 1: $1"
echo "Argument 2: $2"
echo "All arguments: $@"
echo "Argument count: $#"
while read line; do
  echo "Line: $line"
done
exit 123
# Ping continuously
ping 1.1.1.1