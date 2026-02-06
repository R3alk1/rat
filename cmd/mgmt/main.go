package main

import (
	"io"
	"log"
	"net/http"
)

const HOST = "https://c2-server:8081"

func proxy(w http.ResponseWriter, r *http.Request) {
	target := HOST + r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	request, _ := http.NewRequest(r.Method, target, r.Body)
	responce, err := http.DefaultClient.Do(request)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	defer responce.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	io.Copy(w, responce.Body)
}

func main() {
	fs := http.FileServer(http.Dir("./web"))
	http.Handle("/", fs)
	http.HandleFunc("/api/", proxy)
	log.Println("Management web-interface at http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
