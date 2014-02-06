angular.module("daisserapp", []);
function DaisserController($scope, $timeout, $http) {

	var baseLayers = {
		"OSM": L.tileLayer('http://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
			attribution: 'Map data &copy; <a href="http://openstreetmap.org">OpenStreetMap</a> contributors, <a href="http://creativecommons.org/licenses/by-sa/2.0/">CC-BY-SA</a>',
			maxZoom: 19,
			minZoom: 4
		}),
	};

	angular.extend($scope, {
		map:  L.map('daissermap', {
		//	markerZoomAnimation:false, // markerZoomAnimation uses zoomAnimation value as default
		//	zoomAnimation: false,
		//	fadeAnimation: false,
			fullscreenControl: true,
			worldCopyJump:true,
			zoom: 4,
			center: L.latLng(50, 8.57),
			layers: baseLayers.OSM
		}),
		pointsLayer: L.geoJson(),
		fetch: function() {
			$http.get('./points')
			.success(function(points) 
			{
				$scope.pointsLayer.addData(points);
				$scope.map.fitBounds($scope.pointsLayer.getBounds());
			})
			.error(function(data, status, headers, config)
			{
				console.log("REMOVE");
			});
			$timeout($scope.fetch, 300000);
		},
	});

	$scope.pointsLayer.addTo($scope.map)
	$scope.fetch();
	L.control.layers(baseLayers, {"fabian":$scope.pointsLayer}).addTo($scope.map);
	L.control.scale().addTo($scope.map)
}

