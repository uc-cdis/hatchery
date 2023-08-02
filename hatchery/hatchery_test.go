package hatchery

//TODO:

/*
* getWorkspaceStatus
	*getPayModelsForUser
		* returns nil
		* returns paymodels with current paymodel.ecs
		* returns paymodels with current paymodel -- nil
		* returns paymodels with current paymodel.ecs == false
	* statusK8sPod and status ECS -- Need to mimic mockFunction.call(numTimes)

Find a way to send w (Writer object) such that the response can be tested during assertions

* SetPaymodels
	* mock w, r to provide userName and id
		* id being empty should return in an error
	* mock getWorkspoaceStatus, setCurrentPayModel
		* status with status.Status == "Running" return error
		* status with status.Status == "Not Found" should call setCurrentPayModel and return the mock currentPayModel
* 	resetPayModels
	* mock w,r
		* r.Method not being post must return an error
	* mock getWorkspoaceStatus, setCurrentPayModel
		* status with status.Status == "Running" return error
		* status with status.Status == "Not Found" should call "resetCurrentPayModel" and return the mock currentPayModel

* launch method
	* Flow exploration TBD
* terminate method
	* Flow exploration TBD

*/
