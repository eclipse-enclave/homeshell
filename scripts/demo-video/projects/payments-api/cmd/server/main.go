package main

import (
	"log"
	"net/http"

	"example.com/payments-api/internal/refunds"
	"example.com/payments-api/internal/webhooks"
)

func main() {
	hooks := webhooks.NewDeliverer(http.DefaultClient)
	mux := http.NewServeMux()
	mux.Handle("POST /refunds", refunds.NewHandler(refunds.NewMemoryStore(), hooks))
	log.Fatal(http.ListenAndServe(":8080", mux))
}
