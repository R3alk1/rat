package main

import (
	"io"
	"log"
	"net/http"
)

const HOST = "http://c2-server:8081"

func proxy(w http.ResponseWriter, r *http.Request) {
	target := HOST + r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	request, err := http.NewRequest(r.Method, target, r.Body)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	request.Header = r.Header
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	defer response.Body.Close()
	for key, values := range response.Header {
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(response.StatusCode)
	io.Copy(w, response.Body)
}

func main() {
	fs := http.FileServer(http.Dir("./web"))
	http.Handle("/", fs)
	http.HandleFunc("/api/", proxy)
	http.HandleFunc("/download/", proxy)
	log.Println("Management web-interface at http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
