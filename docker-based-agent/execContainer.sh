#!/bin/bash

# get parameters
name=""
command=""
time=120
while getopts n:c:t: flag
do
    case "${flag}" in
        n) name=${OPTARG};;
        c) command=${OPTARG};;
        t) time=${OPTARG};;
        *) exit 1;;
    esac
done

# validate parameters
if [ "$name" = "" ];
then
  echo "parameter -n cannot be empty, please specify a name."
  exit 1
fi

if [ "$command" = "" ];
then
  echo "parameter -c cannot be empty, please specify a command."
  exit 1
fi

# Path to ServiceAccount token
SERVICEACCOUNT=/var/run/secrets/kubernetes.io/serviceaccount

# Read this Pod's namespace
NAMESPACE=$(cat ${SERVICEACCOUNT}/namespace)

# wait until pod is ready
echo "Querying pod state every 5s up until ${time}s"
counter=0
ready=1
# as long as pod state is not running and it didn't take longer than $time seconds yet
while (( ready == 1 && counter < $time ))
do
  echo "kubectl get pod $HOSTNAME --output='jsonpath={.status.phase}' -n ${NAMESPACE}"
  OUTPUT=$(kubectl get pod "$HOSTNAME" --output="jsonpath={.status.phase}" -n "${NAMESPACE}")
  echo "$OUTPUT"
  #if pod is in state running, then continue, otherwise query again in 5s
  if [[ $OUTPUT == *"Running"* ]];
  then
    ready=0
  else
    echo "Pod not running yet, waiting for 5s to query again."
    sleep 5
    counter=$(( counter + 5 ))
  fi
done

# finish script depending on the result of the while loop
if [ $ready -eq 0 ];
then
  echo "Pod ready to use, have fun!"
else
  echo "Couldn't create pod, please try again."
  exit 1
fi

kubectl exec "$HOSTNAME" -c "$name" -- sh -c "
      export BUILD_BUILDID=\"$BUILD_BUILDID\";
      export BUILD_DEFINITIONNAME=\"$BUILD_DEFINITIONNAME\";
      export BUILD_SOURCEBRANCHNAME=\"$BUILD_SOURCEBRANCHNAME\";
      export TF_BUILD=\"$TF_BUILD\";
      export SYSTEM_ACCESSTOKEN=\"$SYSTEM_ACCESSTOKEN\";
      export SYSTEM_DEFINITIONID=\"$SYSTEM_DEFINITIONID\";
      export SYSTEM_TEAMPROJECT=\"$SYSTEM_TEAMPROJECT\";
      export SYSTEM_COLLECTIONURI=\"$SYSTEM_COLLECTIONURI\";
      export SYSTEM_PULLREQUEST_SOURCEBRANCH=\"$SYSTEM_PULLREQUEST_SOURCEBRANCH\";
      $command"
