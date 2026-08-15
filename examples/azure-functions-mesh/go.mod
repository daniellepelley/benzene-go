module github.com/daniellepelley/benzene-go/examples/azure-functions-mesh

go 1.24.7

require (
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.20.0
	github.com/Azure/azure-sdk-for-go/sdk/messaging/azeventhubs/v2 v2.0.2
	github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus v1.10.0
	github.com/Azure/azure-sdk-for-go/sdk/messaging/eventgrid/azeventgrid v1.0.0
	github.com/daniellepelley/benzene-go v0.1.0
	github.com/daniellepelley/benzene-go/azureeventgrid v0.1.0
	github.com/daniellepelley/benzene-go/azureeventhub v0.1.0
	github.com/daniellepelley/benzene-go/azureservicebus v0.1.0
)

require (
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.11.2 // indirect
	github.com/Azure/go-amqp v1.5.0 // indirect
	golang.org/x/net v0.47.0 // indirect
	golang.org/x/text v0.31.0 // indirect
)
