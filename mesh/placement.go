package mesh

import "os"

// DetectPlacement identifies where this process runs from each platform's documented
// environment variables (mesh.md §4.3), so placement needs no configuration on the three
// clouds this module ships bindings for:
//
//   - AWS Lambda: AWS_LAMBDA_FUNCTION_NAME (a defined Lambda runtime variable); region
//     from AWS_REGION.
//   - Azure Functions: FUNCTIONS_CUSTOMHANDLER_PORT, the same documented custom-handler
//     contract variable the azurefunctions package is driven by. The Functions host does
//     not expose its region through a documented environment variable, so Region is left
//     empty rather than guessed.
//   - Google Cloud Run: K_SERVICE (a documented Cloud Run/Knative variable). Cloud Run
//     exposes region only via the metadata server, not the environment, so Region is
//     left empty rather than guessed.
//
// When none match, the placement is "self-hosted". A service that knows better (or runs
// on a platform not listed) sets ServiceInfo.Placement explicitly, which bypasses
// detection entirely.
func DetectPlacement() Placement {
	return detectPlacement(os.Getenv)
}

func detectPlacement(getenv func(string) string) Placement {
	switch {
	case getenv("AWS_LAMBDA_FUNCTION_NAME") != "":
		return Placement{Cloud: "aws", Region: getenv("AWS_REGION")}
	case getenv("FUNCTIONS_CUSTOMHANDLER_PORT") != "":
		return Placement{Cloud: "azure"}
	case getenv("K_SERVICE") != "":
		return Placement{Cloud: "gcp"}
	}
	return Placement{Cloud: "self-hosted"}
}
