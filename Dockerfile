# Use the official Golang image as the base
FROM golang:1.20-alpine

# Set the working directory inside the container
WORKDIR /app

# Copy the Go modules files and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of your application source code
COPY . .

# Build the Go application
RUN go build -o ushico-websocket-server .

# Expose the port your application listens on (e.g., 8085)
EXPOSE 8085

# Set environment variables if needed (optional)
# ENV ENVIRONMENT=production

# Command to run when starting the container
CMD ["./ushico-websocket-server"]
