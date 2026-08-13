package editor

import "math"

// ColorMatrixV1 is a row-major 4x5 matrix operating on unpremultiplied sRGB
// channels. Effects are composed in document order and clamped once after the
// complete stack. This definition is shared with the browser renderer.
type ColorMatrixV1 [20]float64

func IdentityColorMatrixV1() ColorMatrixV1 {
	return ColorMatrixV1{1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 1, 0}
}

func CompileColorMatrixV1(effects []EffectV2) ColorMatrixV1 {
	result := IdentityColorMatrixV1()
	for _, effect := range effects {
		if !effect.Enabled {
			continue
		}
		result = multiplyColorMatrixV1(effectColorMatrixV1(effect), result)
	}
	return result
}

func ApplyColorMatrixV1(matrix ColorMatrixV1, red, green, blue, alpha float64) (float64, float64, float64, float64) {
	return clampChannel(matrix[0]*red + matrix[1]*green + matrix[2]*blue + matrix[3]*alpha + matrix[4]),
		clampChannel(matrix[5]*red + matrix[6]*green + matrix[7]*blue + matrix[8]*alpha + matrix[9]),
		clampChannel(matrix[10]*red + matrix[11]*green + matrix[12]*blue + matrix[13]*alpha + matrix[14]),
		clampChannel(matrix[15]*red + matrix[16]*green + matrix[17]*blue + matrix[18]*alpha + matrix[19])
}

func effectColorMatrixV1(effect EffectV2) ColorMatrixV1 {
	matrix := IdentityColorMatrixV1()
	switch effect.Type {
	case "exposure":
		gain := math.Exp2(effect.Parameters["stops"])
		matrix[0], matrix[6], matrix[12] = gain, gain, gain
	case "contrast":
		gain := math.Exp2(effect.Parameters["amount"])
		offset := (1 - gain) / 2
		matrix[0], matrix[6], matrix[12] = gain, gain, gain
		matrix[4], matrix[9], matrix[14] = offset, offset, offset
	case "saturation":
		gain := 1 + effect.Parameters["amount"]
		inverse := 1 - gain
		const redLuma, greenLuma, blueLuma = .2126, .7152, .0722
		matrix[0], matrix[1], matrix[2] = redLuma*inverse+gain, greenLuma*inverse, blueLuma*inverse
		matrix[5], matrix[6], matrix[7] = redLuma*inverse, greenLuma*inverse+gain, blueLuma*inverse
		matrix[10], matrix[11], matrix[12] = redLuma*inverse, greenLuma*inverse, blueLuma*inverse+gain
	case "temperature":
		warmth := effect.Parameters["kelvin_delta"] / 10000
		matrix[0], matrix[6], matrix[12] = 1+.2*warmth, 1+.05*warmth, 1-.2*warmth
	}
	return matrix
}

func multiplyColorMatrixV1(left, right ColorMatrixV1) ColorMatrixV1 {
	var result ColorMatrixV1
	for row := 0; row < 4; row++ {
		for column := 0; column < 4; column++ {
			for inner := 0; inner < 4; inner++ {
				result[row*5+column] += left[row*5+inner] * right[inner*5+column]
			}
		}
		result[row*5+4] = left[row*5+4]
		for inner := 0; inner < 4; inner++ {
			result[row*5+4] += left[row*5+inner] * right[inner*5+4]
		}
	}
	return result
}

func clampChannel(value float64) float64 {
	return math.Max(0, math.Min(1, value))
}
