# Use the official Golang image as the base image
FROM golang:alpine as builder

# Set the working directory
WORKDIR /app

# Copy go.mod and go.sum files into the container
COPY go.mod go.sum ./

# Download all the dependencies
RUN go mod download

# Copy the source code into the container
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o main .

# Start a new stage with a minimal base image
FROM alpine:latest

# Set the working directory
WORKDIR /app

# Copy the binary from the builder stage
COPY --from=builder /app/main /app/main

# Expose the port the application will run on
EXPOSE 80

# Run the application
CMD ["/app/main"]
