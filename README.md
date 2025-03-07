# Go starter template
This is meant to be a quickstart for web services in Go with OIDC based authentication.

## Requirements
- **GNU Make:** *optional, enables some useful commands*
- **Docker**: *required*
- **Docker Compose:** *required*

## Setup
Execute the following steps to set the project's configuration for the first time.

### Step 1
Create the **.env** file in the **project's root** directory.

**Note:** *use the .env.dist file as template.*

### Step 2
Create the **PEM key** files in the **project's root** directory.

**Note:** *use the make command to quickly generate the credentials.*

### Step 3
Create the **docker-compose.yaml.local** file in the **project's root** directory.

**Note:** *use the docker-compose.yaml.local.dist file as template. Don't forget to fill the .env file with the PEM keys volumes target path*

### Step 4
Run the following command from the **project's root** directory:
```
make init
```
**Note:** *this will set the name for the project's Go module.*
