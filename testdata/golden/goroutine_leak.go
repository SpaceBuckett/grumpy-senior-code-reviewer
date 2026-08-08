package golden

func FastestResponse(endpoints []string, fetch func(string) string) string {
	results := make(chan string)
	for _, endpoint := range endpoints {
		go func(endpoint string) {
			results <- fetch(endpoint)
		}(endpoint)
	}
	return <-results
}
