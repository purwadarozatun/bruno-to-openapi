go mod tidy

echo "Building the binary..."
go build -o bin/bruno-to-openapi . 

echo "Copying binary to /usr/local/bin/"
cp bin/bruno-to-openapi /usr/local/bin/

