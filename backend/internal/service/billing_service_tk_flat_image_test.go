//go:build unit

package service

import "testing"

func TestTkIsFlatPerImageModel(t *testing.T) {
	t.Parallel()
	if !tkIsFlatPerImageModel("doubao-seedream-5.0-flash") {
		t.Fatal("Seedream 5.0 Flash official list is one price per image")
	}
	if !tkIsFlatPerImageModel("wan2.7-image-pro") {
		t.Fatal("Wan 2.7 image list has no resolution tier")
	}
	if tkIsFlatPerImageModel("qwen-image-3.0-pro") {
		t.Fatal("Qwen Image 3.0 Pro bills distinct 1K and 2K output prices")
	}
	if tkIsFlatPerImageModel("doubao-seedream-5-0-pro") {
		t.Fatal("Seedream 5.0 Pro keeps pixel-tier prices")
	}
}
