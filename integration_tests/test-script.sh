echo "Hello Philmod"

ls /root/test-files/

container-builder-local | grep 'Version:' &> /dev/null
if [ $? == 0 ]; then
  echo "matched"
else
  echo "not matched"
fi


echo "Bye Philmod"
