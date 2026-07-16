package mesh

import "testing"

func TestDetectPlacement_Table(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want Placement
	}{
		{
			name: "aws lambda with region",
			env:  map[string]string{"AWS_LAMBDA_FUNCTION_NAME": "orders", "AWS_REGION": "eu-west-1"},
			want: Placement{Cloud: "aws", Region: "eu-west-1"},
		},
		{
			name: "aws lambda without region",
			env:  map[string]string{"AWS_LAMBDA_FUNCTION_NAME": "orders"},
			want: Placement{Cloud: "aws"},
		},
		{
			name: "azure functions custom handler",
			env:  map[string]string{"FUNCTIONS_CUSTOMHANDLER_PORT": "8080"},
			want: Placement{Cloud: "azure"},
		},
		{
			name: "google cloud run",
			env:  map[string]string{"K_SERVICE": "orders"},
			want: Placement{Cloud: "gcp"},
		},
		{
			name: "no platform markers means self-hosted",
			env:  map[string]string{},
			want: Placement{Cloud: "self-hosted"},
		},
		{
			name: "lambda marker wins over cloud run marker",
			env:  map[string]string{"AWS_LAMBDA_FUNCTION_NAME": "orders", "K_SERVICE": "orders"},
			want: Placement{Cloud: "aws"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(key string) string { return tt.env[key] }

			if got := detectPlacement(getenv); got != tt.want {
				t.Errorf("detectPlacement() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDetectPlacement_ReadsProcessEnvironment(t *testing.T) {
	for _, v := range []string{"FUNCTIONS_CUSTOMHANDLER_PORT", "K_SERVICE"} {
		t.Setenv(v, "")
	}
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "orders")
	t.Setenv("AWS_REGION", "eu-west-1")

	if got := DetectPlacement(); got != (Placement{Cloud: "aws", Region: "eu-west-1"}) {
		t.Errorf("DetectPlacement() = %+v, want aws/eu-west-1", got)
	}
}
