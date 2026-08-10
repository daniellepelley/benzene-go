// Command aws-s3-helloworld is an S3 event-notification consumer Lambda: S3 invokes it whenever an
// object is created in an `uploads` bucket, and it handles the notification. There is no publisher
// here - the "publish" is uploading an object to the bucket (with the AWS CLI, an SDK, the console);
// S3's event notification turns that upload into an invocation this Lambda consumes.
//
// It is the S3 counterpart of examples/aws-kinesis-helloworld, and lives in the root module because
// the awss3 binding is itself zero-dependency: S3 delivers the notification (object metadata only,
// not the object's contents) as plain JSON, so there is no AWS SDK to pull in.
package main

import (
	"context"
	"log"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/awslambda"
	"github.com/daniellepelley/benzene-go/awss3"
)

// objectCreated is the metadata an S3 ObjectCreated event carries (S3 does not include the object's
// contents - fetch those with an S3 client using the bucket + key if you need them).
type objectCreated struct {
	BucketName string `json:"bucketName"`
	Key        string `json:"key"`
	Size       int64  `json:"size"`
	ETag       string `json:"etag"`
}

// objectUploaded handles an ObjectCreated:Put notification for the uploads bucket (the topic is
// "uploads:ObjectCreated:Put"). Returning a failure makes the binding return a Go error, so AWS's
// async-invoke retry reprocesses the event rather than dropping it - handlers must be idempotent.
func objectUploaded(_ context.Context, o objectCreated) benzene.Result[struct{}] {
	if o.Key == "" {
		return benzene.BadRequest[struct{}]("object key is required")
	}
	log.Printf("object uploaded: s3://%s/%s (%d bytes, etag %s)", o.BucketName, o.Key, o.Size, o.ETag)
	return benzene.Ok(struct{}{})
}

// newApp is the composition root both main() and the tests boot from. The topic is
// "{bucket}:{eventName}" - register the specific bucket+event combinations this service consumes.
func newApp() benzene.App[struct{}] {
	return benzene.App[struct{}]{
		GetConfiguration: func() struct{} { return struct{}{} },
		ConfigureServices: func(registry *benzene.Registry, _ *benzene.Container, _ struct{}) {
			if err := benzene.Register(registry, benzene.NewTopic("uploads:ObjectCreated:Put"), benzene.Handler[objectCreated, struct{}](objectUploaded)); err != nil {
				panic(err)
			}
		},
		Configure: func(builder *benzene.ApplicationBuilder, _ struct{}) {
			builder.UsePipeline(benzene.NewPipeline(benzene.RouterMiddleware(builder.Registry)))
		},
	}
}

func main() {
	awslambda.Start(awss3.Handler(newApp().Run()))
}
