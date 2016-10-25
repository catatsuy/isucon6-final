package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/isucon/isucon6-final/portal/job"
)

// serveQueueJob は参加者がベンチマーカのジョブをキューに挿入するエンドポイント。
func serveQueueJob(w http.ResponseWriter, req *http.Request) error {
	if req.Method != http.MethodPost {
		return errHTTP(http.StatusMethodNotAllowed)
	}

	// 18時になったらコンテスト終了なのでジョブを挿入させない
	switch getContestStatus() {
	case contestStatusNotStarted:
		return errHTTPMessage{http.StatusForbidden, "Final has not started yet"}
	case contestStatusEnded:
		return errHTTPMessage{http.StatusForbidden, "Final has finished"}
	}

	team, err := loadTeamFromSession(req)
	if err != nil {
		return err
	}
	if team == nil {
		return errHTTP(http.StatusForbidden)
	}

	if team.IPAddr == "" {
		return errHTTP(http.StatusBadRequest)
	}

	err = enqueueJob(team.ID)
	if err != nil {
		if _, ok := err.(errAlreadyQueued); ok {
			// ユーザに教えてあげる
			return serveIndexWithMessage(w, req, "Job already queued")
		}

		return err
	}

	// TODO(motemen): flash
	http.Redirect(w, req, "/", http.StatusFound)

	return nil
}

// 新しいジョブを取り出す。ジョブが無い場合は 204 を返す
// クライアントは定期的(3秒おきくらい)にリクエストしてジョブを確認する
func serveNewJob(w http.ResponseWriter, req *http.Request) error {
	if req.Method != http.MethodPost {
		return errHTTP(http.StatusMethodNotAllowed)
	}
	benchNode := req.FormValue("bench_node")
	j, err := dequeueJob(benchNode)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return nil
	}
	if j == nil {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	j.URLs, err = getProxyURLs(j.TeamID)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return nil
	}
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(j)
	w.Write(b)
	return nil
}

func servePostResult(w http.ResponseWriter, req *http.Request) error {
	if req.Method != http.MethodPost {
		http.Error(w, "Method Not Allowd", http.StatusMethodNotAllowed)
		return nil
	}
	var res job.Result
	if req.Body == nil {
		http.Error(w, "Please send a request body", http.StatusBadRequest)
		return nil
	}
	err := json.NewDecoder(req.Body).Decode(&res)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return nil
	}
	err = doneJob(&res)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"success":true}`)
	return nil
}
