package genai

import "sync"

// fanOut runs attempt(0..n-1) all at once and collects them in attempt
// order. An attempt that returns no image is a Failure: its own, or its
// error's text. The round fails with the key error only when every attempt
// was refused on the key (isAuth), so nothing came back and nothing was
// charged; one refusal beside a paid attempt never discards the paid one.
func fanOut(n int, attempt func(i int) ([]Image, Failure, error)) ([]Image, []Failure, error) {
	type result struct {
		imgs []Image
		fail Failure
		err  error
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			imgs, fail, err := attempt(i)
			results[i] = result{imgs, fail, err}
		}()
	}
	wg.Wait()

	var imgs []Image
	var fails []Failure
	var authErr error
	refused := 0
	for i, res := range results {
		switch {
		case res.err != nil:
			if isAuth(res.err) {
				refused++
				if authErr == nil {
					authErr = res.err
				}
			}
			fails = append(fails, Failure{Index: i, Reason: res.err.Error()})
		case len(res.imgs) == 0:
			res.fail.Index = i
			fails = append(fails, res.fail)
		default:
			imgs = append(imgs, res.imgs...)
		}
	}
	if n > 0 && refused == n {
		return nil, nil, authErr
	}
	return imgs, fails, nil
}

// one is an attempt's single image as fanOut takes it.
func one(img *Image, fail Failure, err error) ([]Image, Failure, error) {
	if img == nil {
		return nil, fail, err
	}
	return []Image{*img}, fail, err
}
