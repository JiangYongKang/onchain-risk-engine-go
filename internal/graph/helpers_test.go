package graph

import "time"

// makeTimer 返回一个 2 秒超时通道（独立文件放置测试辅助）。
func makeTimer() <-chan time.Time {
	return time.After(2 * time.Second)
}
