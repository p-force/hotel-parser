package handler

import (
	"github.com/p-force/hotel-parser/internal/vercelapp"
	"net/http"
)

func Resume(w http.ResponseWriter, r *http.Request) { vercelapp.Serve("resume", w, r) }
