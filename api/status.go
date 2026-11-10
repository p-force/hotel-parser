package handler

import (
	"github.com/p-force/hotel-parser/internal/vercelapp"
	"net/http"
)

func Status(w http.ResponseWriter, r *http.Request) { vercelapp.Serve("status", w, r) }
