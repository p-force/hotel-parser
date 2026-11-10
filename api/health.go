package handler

import (
	"github.com/p-force/hotel-parser/internal/vercelapp"
	"net/http"
)

func Health(w http.ResponseWriter, r *http.Request) { vercelapp.Serve("health", w, r) }
