package handler

import (
	"github.com/p-force/hotel-parser/internal/vercelapp"
	"net/http"
)

func Plan(w http.ResponseWriter, r *http.Request) { vercelapp.Serve("plan", w, r) }
