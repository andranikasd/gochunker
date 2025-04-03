# Start from the official Golang image as a build stage
FROM golang:1.21 as builder

# Set the working directory inside the container
WORKDIR /app

# Copy go.mod and go.sum first for dependency caching
COPY go.mod go.sum ./
RUN go mod download

# Copy the full source code
COPY . .

# Build the Go app statically
RUN CGO_ENABLED=0 GOOS=linux go build -o gochunker .

# Expose the port the service listens on
EXPOSE 8080

# Run the controller service
ENTRYPOINT ["./gochunker"]
